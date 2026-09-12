---
id: babel-files-its-output
version: 2
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

> **You may not change a topic.** A topic is a Reality Ledger entity, and
> entities are created, split, merged and retired by an attributed operator act
> (§4.8, §4.13). You may file a record under an entity that already exists, or
> *propose* a change and let the operator decide. Your proposal is published as
> an ordinary proposal record, he rules on it the way he rules on every other
> one, and his acceptance is what applies it. A name you use that the ledger
> cannot resolve becomes a proposal, not a new thing.

## Question

The question is what this record is *about*: the topic a reader would look for
it under, months from now, when they remember the subject and not the sentence.
And, when the ledger's topics are themselves wrong for it, which change to them
would make it findable.

A topic is a thing in the world — a repository, a project, a machine, a
service, an organization, a concept the operator actually thinks in. It has a
name, and it has a binding to something real: a remote, a checkout, a hostname,
or a one-sentence definition somebody could disagree with. *A topic is not a
folder.* The directory a session was started in is where the work happened, not
what it was about; a path under `/tmp`, a generated worktree name, and a
scratch checkout are locators, and a locator is evidence about a topic and
never the topic itself.

Four answers are available and exactly one of them is right for any record.

- **File it** under a topic that already exists. This is the answer to prefer
  and the commonest correct one. Say why the record is about that topic.
- **Propose a topic change** when the ledger's topics are wrong for this
  record. There is one output kind for that and it carries an `operation`:
  `create` a topic nobody has named, `split` a topic that names two things,
  `merge` two that name one, or `retire` one that should never have existed.
  It is a proposal for the operator, never a change.
- **No topic**, with the reason, when the record is about nothing in
  particular. Some outputs are about a passing question, about the mechanism,
  about a coincidence nobody will ever look up by name. Saying so is an honest
  result and a completed pass, not a failure and not a skip.
- **No change**, with the ask it answers and the reason, when the operator
  asked for something about a topic and you judge it wrong. It is an answer,
  not a refusal to work: the reason lands as a reply where he asked it.

### Which operation is right

`create` is right when the record is about something no listed entity names,
and only then. An identity listed under *what the scan observed and nothing
names* is evidence for one — it is a repository this host saw with the sessions
and checkouts behind it — and it is evidence only when **this** record is about
it. The busiest unnamed identity on the machine is not a reason to name it
while reviewing a record about something else.

`split` is right when one listed topic demonstrably names two things and this
record belongs to the part that would be separated out. Name the topic being
split in `targets`, and describe the new part the way a `create` describes what
it creates. A split is not for a topic that is merely broad: a repository with
a library and a service inside it is one repository until the records stop
overlapping.

`merge` is right when two listed topics name one thing — the same repository
under two spellings, a service and the host it happens to run on, a concept the
operator named twice. Put the one that disappears first in `targets` and the
survivor second. Nothing is rewritten by a merge: a filing names an entity id
and every reader resolves it through the merge history.

`retire` is right when a topic should never have existed — a workspace name, a
locator that acquired a name, a thing nobody works on and never did. Retiring
re-queues everything filed under it for triage, so the reason has to be that
the topic is *wrong*, never that it is quiet. A topic the operator is simply
not working on is a lifecycle fact he records, not a retirement.

Every operation needs its `reasoning`: why the ledger should change, and why
each topic in `considered` was weighed and rejected. Every name in `targets`
must be a topic you were listed — an invented one is refused as a malformed
result and creates nothing.

### Answering what the operator asked

Sentences the operator told Babel about a topic arrive under *what the operator
asked*, with an id. Answer every one that concerns this record's topics, and
answer only those.

An ask is answered, never obeyed. If you agree with it, the answer is the
`topic` proposal it calls for, with `ask_id` naming the ask — the operator asked
Babel and Babel proposes, which is the whole of §4.13's second reading. If you
judge it wrong, the answer is `no_change` with the `ask_id` and the reason, and
that reason is recorded as a reply on his own sentence. "The cli and the service
are one deployable and no record separates them" is an answer. Splitting a topic
because you were told to, when the records say it is one thing, is Babel
becoming a remote control — which is the failure the second reading closes.

An ask you cannot judge from this record is left alone. It stays unanswered and
reaches the next pass that reads a record it concerns, which is better than an
answer nobody had the material for.

## Inclusion, exclusion, and ambiguity

**What you were shown.** The record as written, its claim, its case, the
evidence it cites, the topics the ledger already holds with their aliases and
bindings, the repositories the cited sessions were in, the reasons earlier
topics were retired and earlier proposals declined, the repository identities
the scan observed that nothing names, and the operator's unanswered asks. The
retired and declined reasons are why Babel got a topic wrong before, and
proposing something they already rule out is the failure that material exists
to prevent.

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

For a `split`, a `merge` or a `retire`, the evidence is what the records
themselves do: which of them the two halves of a split would separate, which
records the two merged topics hold that say the same thing, what is filed under
a topic you would retire. A structural argument about names, with nothing about
records behind it, is a preference about vocabulary.

Counter-evidence to seek against your own answer:

- for every proposal, the existing topic it duplicates — search the listed
  aliases for the words in your proposed name before proposing;
- for every proposal, the declined reasons: an operator who has already refused
  this topic refused it for a reason that has probably not changed;
- for every proposal, the retired reasons: a topic that was retired as "not a
  thing anybody works on" should not come back under a new name;
- for a `create` from an observed identity, whether the counts are a project or
  one afternoon: a single session in a single checkout is a clone, not a topic;
- for a `split`, whether the records of the two halves actually separate, or
  whether every one of them cites both;
- for a `merge`, whether the two topics differ in the thing they bind to — a
  service and its repository are two topics that share a name;
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
5. Is the ask still what he wants? An ask is his wording at the time he wrote
   it, and the records may have moved since. Answer what the material now
   supports and say so.

## Classifications and stopping conditions

The result's own fields are the classification: `filing` with an entity and a
rationale; `topic` with an `operation` from `create`, `split`, `merge` and
`retire`, the `targets` it acts on, the name, kind, identity, binding and
reasoning of anything it would create, the topics `considered`, and the
`ask_id` it answers where it answers one; `no_topic` with a reason; or
`no_change` with an `ask_id` and a reason. There is no fifth answer.

The `identity` of a created topic is a dedup key and matters more than the
name: the normalized remote for a repository, the common directory every
worktree shares when there is no remote, the hostname for a machine, a
lowercase hyphenated slug for a concept. The same thing proposed from two
records must arrive with the same identity, or the operator gets the same
question twice.

Stop as soon as one answer is right:

- Stop at the first existing topic that clearly covers the record. Do not
  enumerate the alternatives you did not need.
- Stop at one proposal. A pass that proposes three changes to the ledger for
  one record has not decided what the record is about.
- Stop at `no_topic` rather than filing under a topic that is merely nearby.
  A wrong filing is worse than an unfiled record: unfiled is a visible backlog,
  and a wrong filing is a false answer on the topic's own page.
- Stop at `skip` only when the record itself is unreadable from here. Being
  unable to choose a topic is `no_topic` with the reason, not a skip.

Never rule, never rank, never vote. Nothing here accepts, rejects, defers or
retires anything, and a filing is not an opinion about whether the record is
any good.

## Cross-session synthesis keys

Group by: the topic filed under; the operation and identity of a proposal; the
repository of the cited sessions; the kind of the record filed; the ask
answered; and the reason a `no_topic` was recorded.

Recurrence means something specific here. The same proposal identity arriving
from several records is the evidence that makes a topic worth creating — that
is the signal, and it accumulates on the proposal rather than in any one pass.
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
- **Proposing a `create` for whatever the scan listed.** The observed
  identities are evidence about the machine, not a backlog to name. Propose one
  only when the record under review is about it; otherwise the operator pays a
  decision for every clone on the disk.
- **Obeying an ask.** An ask carried out without judgement is a topic changed
  by hand with a run's name on it. Agree and propose, or disagree and say why —
  both are answers, and neither is compliance.
- **Splitting or merging on names.** Two similar names are not two things and
  one name is not one thing. The records are the argument; the vocabulary is
  not.
- **Retiring what is merely quiet.** A dormant project is a lifecycle fact the
  operator records, and retiring it re-queues everything filed under it for no
  reason anybody asked for.
- **Inventing a kind.** A created topic is `repository` because a remote was
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

*Created.* A hypothesis about a deployment failing on one host, where the
ledger holds no entity for that host:
`{"topic": {"operation": "create", "name": "hearth", "kind": "machine",
"identity": "hearth", "definition": "the workstation Babel's conductor runs
on", "reasoning": "three cited sessions name this host as where the deployment
failed; the closest existing topics are the repository being deployed, which is
not what the claim is about, and the deployment service, which the claim
distinguishes from the host", "considered": ["babel", "conductor"]}}`.

*Split, answering an ask.* The operator said `topic t/manifold: the cli and the
service are two things`, and the record under review is about the deployed
service:
`{"topic": {"operation": "split", "targets": ["manifold"], "ask_id":
"cmp_7f3a", "name": "manifold service", "kind": "service", "identity":
"hearth/manifold-service", "definition": "the manifold deployment running on
hearth", "reasoning": "the records split cleanly: four cite the library's API
and none of them mentions a deployment, while this one and two others are about
the running service and its host", "considered": ["manifold"]}}`.

*Merged.* Two entities that bind to the same remote under different spellings:
`{"topic": {"operation": "merge", "targets": ["manifold-cli", "manifold"],
"reasoning": "both bind to github.com/atyrode/manifold and the records under
each are about the same repository; manifold is the name the operator uses in
every session"}}`.

*Retired.* A topic that was seeded from a locator:
`{"topic": {"operation": "retire", "targets": ["tmp"], "reasoning": "the topic
is the basename of a scratch directory: the nine records under it cite sessions
in six unrelated repositories, so it names where work happened and not what any
of it was about"}}`.

*No change, answering an ask.* The operator said `topic t/babel: split the
conductor out`, and the records do not support it:
`{"no_change": {"ask_id": "cmp_91bd", "reason": "every record about the
conductor also cites the repository's own code and tests, and none of them
would move: the conductor is a component of babel rather than a second
subject"}}`.

*No topic.* A record observing that two unrelated analyses were produced within
a minute of each other:
`{"no_topic": {"reason": "the observation is about the timing of two runs and
is not about any subject a reader would look up; neither analysis's topic is
what the record claims"}}`.

*A name the ledger does not know.* Filing under `"entity": "atlas"` when no
entity answers to `atlas` is not refused as an error: Babel turns it into a
`create` proposal for `atlas` as an operator-defined subject, with your
rationale as the definition. That is the correct outcome when `atlas` really is
a thing, and a decision the operator has to decline when it is not — which is
why guessing at a name is not free.
