# Babel specification

Babel is a Manifold plugin family. `atyrode.babel` is the baseline — the store, the doors, the
conductor and the machine half — and `atyrode.babel.feed` and `atyrode.babel.watch` are its two
panels. There is no binary, no command line and no web server of Babel's own.

This document states what Babel is. Where a capability is specified here and not built,
[`docs/parity.md`](docs/parity.md) says so and names the issue that tracks it.

## 1. Purpose

Babel is an open-ended exploratory instrument over a data lake of subjects: archived
conversations between an operator and coding agents, the operator's repositories, Babel's own
records, and brokered public sources. The archive was its first subject and remains the record
of intent and process; the repositories are the record of outcome; Babel reads the gap between
them. It helps ideas emerge about:

- the operator's systems and repositories;
- the way the operator communicates and collaborates with agents;
- agent instructions, rules, skills, and reusable processes;
- missing tools, documentation, comprehension layers, and automation;
- product, code, security, operational, and human-interface opportunities;
- effective patterns worth preserving and repeating; and
- Babel's own code, cookbook, retrieval, analysis, and interaction design.

The project closes a creative feedback loop:

> conversations and reality produce hypotheses; exploration connects and challenges them; human
> review decides what becomes useful; the resulting feedback can improve systems, future
> interactions, and Babel itself.

Among everything Babel can be, its axiomatic center is friction: it strives, ever so slightly and
continuously, to reduce the friction between the operator and their agents. Babel exists to
understand what an agent didn't understand, how communication failed to produce success, and
where error arose only from poor systems, poor communication, missing tools, or missing context.
Its posture is a zen advisor, never a magic solution-finder: it helps problems emerge and puts
words on them, suggests remedies as separate addressable proposals, and welcomes the operator's
open-ended complaints to see whether anything actionable can be made of them. Friction with Babel
itself is inside this scope.

Conversation analysis and repository analysis are two entry points into one question — what was
asked, what the agent understood, and what the code became — so a run may start from the archive
with repositories as context or from a repository with the archive as context (§4.9). Babel
points at its subjects rather than copying them: the archive is the one kind it holds as
immutable copies, and every other kind is observed at a pinned fingerprint at run time and
recorded that way in the receipt (§3.1).

Babel does not promise reliable, exhaustive, or objectively correct analytical output. Its hard
guarantees concern archive integrity, containment, provenance, reproducibility, and no mutating
or publishing external effects. Hypotheses, findings, and proposals remain creative, fallible,
incomplete interpretations for human review.

## 2. Product boundary

### 2.1 Babel owns

- archival orchestration: source roots, snapshot cadence, stable host identity, tags, and the
  never-delete retention policy;
- read-only ingestion of session logs, in place, in the format the harness wrote them;
- the session catalog: what exists, where, how large, what it cost, and which snapshot holds it;
- the analysis cookbook: the methods a run performs, versioned, with a claim citing the method it
  used;
- the prompt a run is given, and the contract its answer must satisfy;
- the durable record of everything it has concluded — hypotheses, observations, findings,
  proposals, questions, topics, assessments, rulings and receipts;
- the conductor: what deserves a run, under which ceilings, and what it cost; and
- its own two panels, which are the whole of how Babel is used.

### 2.2 Babel does not own

- the model, the account, the thinking level or the session: a run is a Code session, posted
  through `atyrode.code.runSession`, and Code owns all four;
- the sandbox a session runs in, which is omp's;
- repository encryption, deduplication and snapshot format, which are restic's;
- the operator's agent configuration: Babel never edits instructions, skills, rules or hooks;
- credential custody and placement, which belong to `atyrode/dotfiles` and to Manifold's own
  service bindings; and
- publication of anything. Babel drafts; the operator acts.

### 2.3 Integration boundary with `atyrode/dotfiles`

Dotfiles owns machine identity, secret retrieval, storage and payload-key placement, and
scheduling. Babel is vault-agnostic: it never creates, emits, reads or rotates a credential, and
the repository password and object-store credential reach it as one storage document delivered
through Manifold's service binding for the job that needs it. Babel ships no substitute and
prescribes no vault tool. Dotfiles does not author Babel's contract.

### 2.4 Primary interaction model

Babel is used through two Manifold panels: **Feed**, the front page and every record, and
**Watch**, what Babel is doing and what it is set to do (§8). Every capability is reachable by
moving through them; none lives only in a place the operator has to remember.

### 2.5 Cross-machine continuation

An archived session can be resumed in a harness by rehydrating the retained original bytes
(§6.8). Exact machine and workspace restoration is not promised, and Babel never launches an
agent: it produces the material and the operator continues the conversation.

### 2.6 Analysis execution boundary

A run reads a **material**: an immutable, sealed selection of session logs, prepared by the
`prepare` job and bound into the session's sandbox read-only. The material's index records, per
session, the selector Babel filed it under, the file it occupies and the digest it was served at.
Every citation a run makes is checked against that index: a path the material does not name, or a
digest that does not match, refuses the whole answer as `unknown-reference`.

A run holds no credential, reaches no network of Babel's, and mutates nothing. It reads the
material, answers in one fenced block, and the session ends.

### 2.8 Manifold plugin boundary

Babel is a plugin family and holds exactly what a plugin holds. Its server half answers doors and
runs the conductor on whatever wake the hub gives it; its machine half is a binary the hub
installs and runs as a job on a named machine; its panels are React halves the shell renders.
Babel holds no open database handle, no long-lived process, no port and no credential: a `batch`
is its transaction, a job is its process, and a service binding is its secret.

A run reaches a model through Code. Babel composes the prompt and nothing else about the session
— no model, no thinking level, no account — and posts it with `ctx.actions.call` on a declared
dependency. `atyrode.babel` declares `atyrode.code` required, so a hub that cannot compose Code
cannot install Babel.

Babel declares no action that could mutate anything outside itself. The store it writes is its
own, and the only external effect any door has is posting or stopping a session.

## 3. Source data and trust model

Babel's core is harness-agnostic. OMP, Codex and Claude Code implement source adapters over one
metadata, normalized-event, provenance, hypothesis, observation, finding and proposal model, and
Babel's own analysis sessions are a fourth harness over that same model (§5.7). The three are the
first adapters rather than the boundary: the harness set is open and declared in one place
(§6.8).

Each adapter always preserves and selectively retrieves the raw chat logs. OMP is the reference
and highest-fidelity adapter. Codex and Claude Code metadata extraction is best effort where
formats are undocumented, unstable or incomplete, but inability to derive a title, workspace,
lifecycle state or artifact closure never excludes the raw transcript from backup or later
analysis. Every catalog row records adapter version and metadata-completeness flags instead of
pretending parity.

The catalog separates a portable common shape from versioned adapter metadata. Common fields —
title, workspace, creation and modification times, lifecycle state, repository fingerprint — are
nullable. Missing values remain null with an explicit completeness reason; adapters never
synthesize a value merely to satisfy a shared shape. Historical captures are addressed by restic
snapshot ID plus the session's source paths.

All archive content is untrusted data. A transcript can contain malicious instructions copied
from issues, web pages, repositories, tool output or prior agents. Babel and the sessions it
posts treat transcript text only as quoted evidence, never as instructions.

The archive can contain secrets, private source code, personal data and attachments. Therefore:

1. ingestion happens locally, on the machine that holds the sessions;
2. the material a run reads is a bounded selection recorded on the run row, not the corpus;
3. a run's model and account are the Code profile the operator chose, and the choice is his;
4. logs never contain raw transcript bodies or credentials; and
5. nothing a run produces leaves the hub.

The public repository and CI contain only generated synthetic fixtures. Real operator
transcripts, titles, paths, catalogs, credentials and analysis outputs are never committed.

**A deterministic secret preflight over material is specified and not built** — see
`docs/parity.md`, the `preflight/` row. Until it exists, the disclosure boundary is the operator's
choice of Code profile and nothing else.

### 3.1 Repository subjects

Repositories are the first pointed subject kind (§4.9). The operator's local checkouts and GitHub
remotes are observed, never copied into the archive: a run pins a fingerprint — the commit, and
the dirty state where a working tree exists — and reads at that pin, and the receipt records the
pin.

Reading a repository has three modes, and they are run-kind choices rather than one rule:

1. **commit-pinned** is the default: the run reads the pinned commit's tree, and the checkout's
   dirty state — modified, staged and untracked paths, without their content — is recorded as a
   fact in the receipt;
2. **working-tree** content is read only under an explicit per-run grant, because the working tree
   is where unfinished and uncommitted material lives; and
3. **GitHub remotes** are read through a read-scoped token delivered like any other secret. The
   session never holds it.

Babel never touches the operator's checkout: no `fetch`, `checkout` or `worktree` inside it, no
lock, no hook. Materialization is a disposable copy of the pinned tree, discarded with the run.

Repository content is untrusted data in exactly the sense archive content is, and more pointedly
so: a repository carries agent instructions — `CLAUDE.md`, `AGENTS.md`, skills, rules, hooks,
prompts — written to be obeyed. The rule of this section extends verbatim: Babel and its sessions
treat repository text as quoted evidence, never as instructions, and nothing in a subject is ever
loaded as a skill, rule or hook of the run that reads it.

A local subject is observable only from the machine that has it. A preparation whose focus or
context names a local checkout is host-pinned, and a run scheduled where its subject is absent is
refused rather than degraded to a guess.

Other kinds with a locator — services, hosts, documentation sites — are admitted as subject kinds
and are facts-only until an observer, a fingerprint and a disclosure story exist for the kind; no
run can focus them before that.

## 4. Conceptual model

Babel distinguishes five layers so that unconstrained ideas can emerge without guesses becoming
facts merely through repetition.

### 4.1 Source record

An immutable, normalized event or artifact with:

- source kind and adapter version;
- host, workspace, session and event identifiers where available;
- source path and content digest;
- timestamp and participant/tool role;
- normalized text or artifact metadata; and
- a locator capable of recovering the original evidence.

### 4.2 Candidate hypothesis

An idea worth investigating, preserved even when it is speculative, uncategorized, duplicated or
not selected within the current run's budget. It records its origin cues, generating run,
provisional labels, novelty/priority signals, and a status: `untriaged`, `deferred`, `promoted`,
`rejected`, `superseded` or `retired`. Its status is the newest row of an append-only history, and
a candidate is written with its first `untriaged` event.

Hypotheses form a durable frontier with typed links such as `derived_from`, `corroborates`,
`contradicts`, `supersedes` and `duplicates`. Sorting never deletes a hypothesis. A candidate may
develop only through the path **hypothesis → one or more observations → finding → proposal**;
developed hypotheses never skip observations.

### 4.3 Observation

A provenance-bearing claim over session, repository, experiment or research evidence. An
observation carries immutable evidence locators, claim category, confidence, impact, recipe
provenance, and explicit counter-evidence or a statement that there is none. It cannot exist
without evidence, and it hangs off exactly one hypothesis.

### 4.4 Finding

One or more related observations consolidated across relevant sessions, repositories, experiments
or research sources. A finding explains the pattern, counter-evidence, recurrence where
applicable, affected scope, and why it matters. Findings are deduplicated but retain all
supporting observations.

### 4.5 Proposal

A proposal is the canonical private review artifact: a human-reviewable possible improvement
suggested by one or more findings, or by the candidate beside it. It contains:

- a concise title, problem statement and proposed outcome;
- linked hypotheses or findings and their provenance;
- applicability and temporal status;
- supporting and conflicting material, uncertainty, impact and estimated scope;
- risks, unresolved questions, prerequisites and suggested verification criteria; and
- a privacy classification.

A proposal is not an issue, document or instruction and has no external side effect.

### 4.6 Output projections

A proposal can be rendered for a destination: a sanitized issue draft, a cross-system improvement
brief, an operator note, a skill or runbook draft, an investigation brief, an effective-pattern
note, a private security brief, or an **agent brief** — the problem, the proposed outcome,
acceptance criteria, and evidence locators an agent can open rather than excerpts it must trust.
A proposal may have several destinations or none, and rendering never changes its canonical
record.

Babel neither publishes a projection nor launches an agent at one. **Rendering is specified and
not built** — `docs/parity.md`, the `review/` row.

### 4.7 Persistent review and refinement

Every record is an immutable row in the hub's store, append-only by trigger: a record is never
edited and never deleted, and a correction is a supersession carrying its predecessor. Rulings
are append-only disposition events — `accept`, `reject`, `defer`, `duplicate`, `reopen`, `refine`
— and the newest per record is its standing. A rejection never deletes a record. Operator context
is attributed guidance, not independent evidence.

A refinement is a separately reviewable proposal that names the exact revision and JSON Pointer it
would change and the replacement it proposes, linked to its target rather than editing it. A
policy depth bound keeps recursive refinement from becoming an unbounded obligation.

### 4.8 Reality Ledger, entity identity, and Questions

Babel models reality as a versioned **Reality Ledger**, not freeform model memory. Stable entities
represent projects, repositories, machines, services, providers, environments, organizations and
other operator-defined subjects. Each entity has a global ID, typed aliases and relationships, and
append-only merge/split history, so repository renames, paths, chat terminology and service moves
do not lose identity and mistaken resolutions remain reversible.

A fact is an immutable revision carrying subject, predicate, typed value or object entity,
validity window, observation time, provenance locator, authority, confidence, sensitivity, status
and superseded/disputed links. States are `proposed`, `active`, `superseded`, `disputed` and
`stale`. Lifecycle, ownership and analysis policy remain separate predicates — `active` versus
`maintenance-only` versus `dormant` versus `retired`, `owned` versus `external`, `normal` versus
`learn-only` versus `excluded` — and lifecycle never silently implies an expenditure policy; an
explicit versioned focus rule performs that mapping.

Only attributed operator acts and configured trusted sources may authorize facts. Git activity,
conversations, repository inspection and Babel's own analysis remain observations or proposed
revisions rather than authority.

Entities are created by an attributed operator act and nothing else creates one: a fact names a
subject that must already exist, and a question names targets the ledger resolves rather than
mints. Raising a question is the one ledger write that needs no author, because a question
authorizes nothing — it is a request that someone else authorize something, which is why an
analysis run may raise one and may not answer it.

A run's answer may carry questions naming their subjects the way the corpus named them, the
predicates at issue, and the candidate each one holds up. A question a run raises is recorded as
`acquire-context`, expects the operator's authority, and is classed from the work it blocks rather
than from how the run graded it: blocking when it names a candidate it holds up, curiosity
otherwise. A self-graded class would make every question blocking within a week.

Every answer is retained verbatim as an attributed immutable event. **Answer interpretation into a
reviewable multi-action plan is specified and not built**: a raw answer stands as the answer, and
no fact, entity resolution or focus-policy change is derived from it without an operator act.

### 4.9 Subjects, focus, and context

Babel's field is a data lake it points at rather than copies. A **subject** is anything with a
locator that Babel can observe at a pinned state; the archive is one subject kind rather than the
field. Four acquisition kinds:

- **held** — the archive: immutable copies Babel made (§6.1), addressed by snapshot;
- **pointed** — a locator observed at a pinned fingerprint at run time: the operator's
  repositories first, and anything else with a locator once its observer, fingerprint and
  disclosure story are defined;
- **derived** — Babel's own records: receipts, the frontier, rulings, this specification and the
  cookbook (§5.7); and
- **public** — brokered external sources. **Not built**: Babel reaches no network of its own
  (`docs/parity.md`, the `research/` row).

A **preparation** names a **focus** — the subjects, at pinned states, the run is about — and a
**context** — the subjects the run may consult. Every focus and context subject is recorded in the
receipt with its fingerprint, so a run is reproducible against the same states even when the
subjects have moved on.

A subject's kind decides what its fingerprint is: a held subject's is its snapshot, a repository's
is its commit plus dirty state, a derived record's is its immutable revision. Non-git subject
kinds are facts-only until their observer exists.

A **watch** is a standing operator interest in a subject, drawn when the subject's material-change
fingerprint moves. **Not built** — `docs/parity.md`.

### 4.10 Recall: the lake as context for agents

**Recall** is the lake read as memory by an agent that is not Babel's: the operator, mid-
conversation with any agent, points at a past discussion and the agent asks Babel for it. Babel
would answer two questions, *where is it* and *what does it say*, with the same retrieval its own
runs use — search returning locators and a bounded excerpt and never a score, then show returning
a bounded excerpt around a locator with a provenance header — and everything it returned would be
delimited and labelled as archived data rather than instruction.

**Recall is not built, and neither is the retrieval it rests on.** There is no index over the
corpus; a preparation selects by recency or by topic. `docs/parity.md` records both, under
`index/` and `research/`. This section is the design recall must satisfy when it is built, not a
description of a capability Babel has.

### 4.11 References, not copies

Every artifact Babel stores references its sources rather than copying them. A reference resolves
to one of exactly two ends: a real-world source — a commit, a file and a line in it, a repository,
anything Babel can name and pin at a state — or a located excerpt of a transcript. There is no
third end.

A locator carries a record's path, its line, its byte offset, and the digest of the record's own
bytes, so a citation is content-addressed against the archive rather than positional against a
file that may have grown since. An excerpt is a view resolved from a locator at the moment it is
needed, never a second copy of the bytes.

The invariant is one sentence: **derived output carries locators, never copied text.** A record's
payload keeps the note and the line that make a claim evidence, and the `cites` edge beside it
carries the session and nothing about where inside it, so a claim stays readable on a hub that
cannot open the conversation it names.

Two consequences follow. Babel analysing its own output cannot amplify duplicated material
(§5.7): a run over prior runs reads their locators and resolves them to the same records, so the
second-order corpus is a graph over the first rather than a fattened copy of it. And an archive of
everything stays proportional to what was actually observed: the held archive grows with the
transcripts Babel saw, while records, findings, proposals and receipts grow with the number of
statements made about them.

### 4.12 Evaluation, reception, and observed outcomes

Babel develops the quality of its existing hypotheses and proposals instead of treating an
ever-growing inbox as the operator's work. Evaluation covers the full lifecycle: open ideas,
operator decisions, implementation, observed outcomes, and material reasons to reconsider earlier
decisions. [`docs/evaluation-lifecycle.md`](docs/evaluation-lifecycle.md) records the
implementation.

**A vote can be the whole contribution.** After reading an exact revision, a reviewer may record
support, opposition or uncertainty without a comment, a new source or an original argument.
Support means "this deserves the operator's consideration"; opposition means "put this lower in
the reading order." Neither means the claim is proven, the remedy works or any action is
authorized. A skip means the reviewer did not assess the item; an unreadable record is not a
downvote.

An assessment is an attributed, append-only record naming the subject kind and immutable revision,
the review assignment and run, the model and recipe versions, and what was consulted. Retries of
one assignment do not mint extra votes. A run cannot boost an alternative it has just authored. A
changed opinion is a linked correction retaining its predecessor.

**Coverage is a first-class result, not an inference from score.** Distinguish **never reviewed**,
**reviewed at this revision**, and **needs re-review**, with blocked or not-applicable reasons
visible. A vote can satisfy a reception review but cannot stand in for evidence checking or
outcome verification. Lack of an evaluator is an explicit coverage gap, not completion.

**Reception is not evidence strength.** Show support, opposition, uncertainty and completed-review
counts together; show skips separately. An unreviewed idea is unknown, not unpopular. Repeated
agreement is reception, not repeated evidence. No fraction of favorable model votes is labelled as
a probability of correctness, and using several models is not proof of independence. Operator
acceptance indicates a choice, not a successful outcome.

The human reading order and the next-review selection are different projections (§5.8). The
ordering is explainable and versioned: it cannot accept, reject, defer, mark a duplicate, delete
or shelve a record.

Learning from operator feedback preserves its meaning and scope. "Not now," "wrong problem," and
"right problem, wrong remedy" are different reasons, not one negative signal. An opened card, a
dwell time, an ignored item and an absent answer are not consent, endorsement or refusal.

**Acceptance, implementation and success are separate.** For accepted proposals Babel may append
an implementation or outcome assessment with the exact revision, criterion version, source
locators, as-of time, environment and uncertainty. That is authority to report what was observed,
never authority to implement. A merge is not deployment; deployment is not proof that the intended
outcome occurred. An outcome is verified only within its stated scope.

Outcome assessments are append-only and contestable. A material change affecting a deferred,
rejected or verified proposal creates a linked **Reconsider** item stating what changed; it does
not reverse the prior decision or silently reopen the proposal.

**The drawn lane dispatches; no live drawn review has run yet.** The conductor draws an
assignment, claims it under a fence, blinds the projection it sends and posts the review as a
Code session. Every rehearsal of that lane so far has been against fixtures, so the reception
this section specifies is proven synthetically and the corpus's existing votes are the crossing's.
`docs/parity.md` states the evidence boundary.

### 4.13 Topics: what a record is about

A **topic** is what a record is about, and it is a Reality Ledger entity (§4.8) and nothing else:
a repository, a project, a machine, a service, a concept — anything with a global ID, aliases, a
reason for existing and a binding to something real. *A topic is not a folder.* The workspace a
session was started in is where work happened, not what it was about; a path under `/tmp` or a
worktree's generated name is a locator, and a locator is evidence about a topic, never the topic.

**Babel proposes identity; only the operator creates it.** A run may name subjects only through
aliases the ledger already holds, and a name it cannot resolve is a question rather than a new
thing. A **topic question** names the records it would file, the entity it proposes — kind,
binding, aliases, and why — and the entities it considered and rejected. Accepting it creates the
entity and files the records in one act. Declining it, with the reason kept verbatim, suppresses
the same proposal until materially new evidence exists.

**Filing is a link.** A record's membership in a topic is a filing — record to entity, carrying a
rationale and its author — produced by the run that wrote the record, by the filing recipe, or by
the operator, and append-only: a re-filing supersedes, a withdrawal is a row, and the history of
where a record was filed and why is readable. A record may be filed under several topics and under
none; *unfiled* is an honest state and the triage backlog, not a bin.

**Interest is a fact about the world, not a preference knob.** The operator's stance toward a topic
is recorded as lifecycle and analysis-policy facts on the entity — *working on it*, *keep an eye*,
*not now*, *excluded* — each an attributed act with a reason kept verbatim. A versioned focus rule
maps lifecycle to expenditure, so *not now* moves the draws elsewhere without deleting a record, a
filing or the topic. *Not interested is a signal, not a deletion.*

**Split, merge, retire — with reasons.** A topic that names two things is split, two that name one
are merged, and one that should never have existed is retired, each through append-only history
with an attributed reason, reversible because nothing is edited. Retiring re-queues the topic's
filings for triage.

**Babel files its own output.** A cookbook recipe, *Babel files its output*, runs as its own draw
kind: it reads open records that are unfiled or filed only by a heuristic, and for each either
files it under an existing entity with a rationale, raises a topic question, or records *no topic*
with a reason, because some outputs are about nothing in particular and saying so is the honest
result. Until it has run, a deployment may seed filings from repository identity alone — the one
binding observable without a model — and must label those filings as heuristic.

**Everything about a topic goes through Babel.** A new topic, a split, a merge and a retirement are
one output kind — a *topic proposal* — produced by a run, carrying Babel's reasoning and the
records it would file, reviewed like every other record; the operator's ruling on it is the same
ruling he gives a proposal. No surface offers a button that creates, splits, merges or retires a
topic directly, because a topic changed by hand is a change Babel did not see and cannot explain.

**Babel handles Babel.** Babel is built to improve itself recursively, and the boundary of that
recursion is the operator: every change to what Babel knows, files, ranks or does is an output that
went through its own pipeline — hypothesis, observation, finding, proposal — was reviewed by its
own reviewers, and was accepted or rejected by the operator from the front page. The least magic
wins: a constant a person picked beats a heuristic, and a heuristic beats a model, until the
evidence says otherwise.

**Observations are evidence, not posts.** Every observation hangs off exactly one hypothesis,
findings consolidate them, proposals address findings; an observation has no standing of its own
and takes its hypothesis's fate. The feed lists what can be ruled on — hypotheses, findings,
proposals, questions — and an observation is reached at the evidence depth of the record it
supports (§8.6), never as a row of its own. Nothing is stale by a clock: a record is stale because
Babel's reviewers found it so, because the topic it is filed under is *not now* or retired, or
because a newer record supersedes it.

## 5. Analysis cookbook

The cookbook is executable exploratory policy, not a fixed taxonomy or one opaque prompt. It gives
analysis productive starting structures while preserving arbitrary emergence. It has three asset
kinds:

- **investigation policies** define shared retrieval, experimentation, challenge, temporal and
  synthesis techniques;
- **domain lenses** define useful questions, evidence rubrics, exclusions and classifications
  without limiting what discovery may propose; and
- **meta recipes** explore Babel's cookbook, analysis process, prior outputs and reviewer
  feedback.

### 5.1 Recipe contract

A recipe is a reviewable Markdown document with machine-readable front matter:

```yaml
id: outcome-integrity
version: 1
kind: lens
scope: [session, corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read, sandbox-exec]
default: true
```

The body defines the question and why it may be fruitful; inclusion, exclusion and ambiguity
guidance; cues useful when sorting emergent hypotheses; evidence and counter-evidence to seek;
temporal checks; suggested classifications and stopping conditions; cross-session synthesis keys;
known failure modes; and examples. These are guidance, not proof that the lens is exhaustive or
that complying with it makes an answer correct.

A recipe never selects a provider or model. Semantic behaviour changes require a version
increment, because a claim cites the method it used as `id@version`.

**Where a recipe lives.** A hub reads its recipes from the policy document the operator installed,
in its `review.recipes` block: each entry is an id, a version, a title, the one line its question
asks, whether it runs by default, and its whole body, which the prompt writes verbatim. The
repository's copy is [`atyrode.babel/store/recipes.seed.json`](atyrode.babel/store/recipes.seed.json),
produced by [`atyrode.babel/tools/seed-recipes.ts`](atyrode.babel/tools/seed-recipes.ts)
from a cookbook-shaped directory. A hub whose policy names no recipe with a body can start no
exploration, and says so.

### 5.2 Open discovery and the hypothesis frontier

Discovery is deliberately divergent. Within the approved evidence boundary it may emit any
candidate hypothesis without first fitting a known lens, category, expected proposal type,
evidence threshold or likelihood score. Every candidate and its origin is persisted.
Classification, clustering, deduplication and priority sorting happen afterward; an uncategorized
candidate remains valid, and recurring valuable uncategorized candidates may justify a new lens.

Investigating a hypothesis may emit further hypotheses. Babel adds them to the durable frontier
and records their relationships rather than forcing the current job to finish every branch. Finite
runs defer the unexplored frontier; they do not erase it.

Sorting optimizes operator attention rather than sanitizing ideas. Novelty, potential value,
uncertainty and similarity estimates affect ordering only. Candidates remain browseable with the
model's original wording, provenance and later review outcome.

### 5.3 Experimental chaos runs

Exploration defaults to a `clean` run. An explicitly selected `chaos` run injects unrelated
perturbation material during divergent discovery to test whether forced association yields ideas
the linked clean control misses. A **chaos atom** is a bounded stimulus with provenance and a
declared type; selection as an atom never authorizes executing its code. An atom cannot support
the hypothesis it induced: before promotion, a chaos-origin candidate must survive a targeted
clean reinvestigation that omits every atom. Chaos defaults off. **Not built** —
`docs/parity.md`.

### 5.4 Shared investigation techniques

After emergence, an investigator may:

1. search other discussions, prior hypotheses and findings, repository snapshots, history, tests
   and authorized public research for related concepts;
2. seek corroboration, contradictions, alternative explanations, and older or newer states;
3. distinguish what a conversation claimed from what was observable then and now;
4. modify a disposable clone and run bounded experiments to test an idea;
5. ask a logically separate challenger to falsify or reframe the hypothesis; and
6. synthesize evidence, dissent, uncertainty and descendant ideas without implying certification.

The challenger is a logically separate job with an intentionally skeptical brief: attack
assumptions, search for disconfirming evidence, test whether either the operator or the agent made
a weak decision, identify opportunity cost, and propose stronger alternatives. It must ground
criticism in evidence, consequences, missing checks or concrete alternatives, and must not infer
character, ability, emotion or intent. It emits objections, counter-evidence or new hypotheses; it
cannot create or promote a finding. A separate synthesizer then judges the exploration and the
critique together, preserves unresolved objections, and is instructed to agree with neither side
by default.

Retrieval rank never becomes evidence strength.

Repository and test observations apply only to the pinned snapshot and command environment
recorded in the receipt. They can establish behaviour in that environment but not infer operator
intent. Unavailable reality evidence remains visible as uncertainty rather than being filled from
conversational confidence.

**Only the explore stage runs today.** The three stages are declared with separate authorities and
the prompt writes each one's instructions, but every launch posts `explore`, so nothing criticizes
a claim and nothing consolidates across runs. `docs/parity.md` names the issue.

### 5.5 Baseline domain lenses

The cookbook contains eight useful but non-exhaustive lenses:

1. **Outcome integrity and unresolved state** — compare requested outcomes, agent claims, observed
   changes and verification; explore incomplete, falsely closed, regressed or genuinely resolved
   work.
2. **Security, privacy and trust boundaries** — explore concrete exposure, unsafe authority,
   credential handling, destructive behaviour and missing containment.
3. **Code health, maintainability, documentation and comprehensibility** — explore dead code,
   complexity, missing tests and docs, misleading abstractions and absent comprehension layers.
4. **Engineering decision quality and operational risk** — revisit assumptions, alternatives,
   constraints, reversibility, recovery and measured consequences without treating hindsight as
   certainty.
5. **Human–agent coordination and avoidable rework** — look for observable ambiguity, ignored
   constraints, repeated corrections, weak handoffs and operator struggle, without diagnosing
   emotion, ability or mental state.
6. **Durable operator model** — explore preferences, constraints and standing conventions while
   distinguishing recurring evidence from one-off instructions.
7. **Reusable practice and capability leverage** — explore successful procedures, skill candidates,
   missing tooling, automation opportunities and their prerequisites.
8. **Effective patterns and enabling conditions** — preserve strategies that produced strong
   outcomes, their enabling context, counterexamples and limits.

Cross-session recurrence is a property available to every lens, not a ninth topic. Five are
enabled by default; the rest run when asked for by name. The lenses organize and inspire what
emerges; they do not constrain discovery.

### 5.6 Developed hypotheses and findings

A developed hypothesis can record a lens or ad hoc framing, recipe versions, origin and lineage,
supporting and conflicting locators, source authority and timestamps, temporal interpretation,
uncertainty, potential value, and the next evidence that could change it. None of those fields
turns it into objective truth or substitutes for observations. A finding is created only from one
or more observations developed while investigating the hypothesis; "developed enough for focused
human review" does not mean "verified correct."

### 5.7 Babel analyzing Babel

A self-analysis run may include Babel's pinned repository, this specification, cookbook versions,
the prior frontier, receipts, reviewer outcomes and evaluation corpora. Recursive lineage and depth
are recorded. A descendant analysis never overwrites its ancestor, and generated material is marked
as model-produced so it cannot become independent corroboration through repetition. Self-analysis
has the same containment and no-publication boundaries as every other run.

Five meta recipes ship, none default-enabled: *Babel improves Babel* evaluates output quality and
acceptance; *Babel tunes itself* examines operator-specific relevance; *Mechanization audit* reads
receipts for inference that could have been retrieval; *Babel triages the queue* is the
role-bounded evaluation contract of §4.12; and *Babel consolidates its backlog* works the
hypotheses a run deferred and nobody came back to. A meta recipe proposes versioned cookbook
changes and never edits the active cookbook.

Each self-improvement finding is classified into exactly one audience dimension — anyone, or this
operator — because those reach different dispositions. Mechanization pressure applies to substrate,
not to making cheaper thoughts or suppressing content diversity.

### 5.8 Backlog-first evaluation within a budget

The evaluation policy favors improving the existing backlog over producing more ideas, within the
scope and spending the operator has authorized. It preserves explicit operator invitations, focus
restrictions, and a protected discovery share so neglected subjects and new evidence can still
enter. It does not enable a duty, start a run, raise a budget or install anything merely because an
inbox exists.

**Weighted randomness, not a popularity loop.** The conductor draws eligible assignments with a
versioned weighted-random policy. Unreviewed and lightly reviewed revisions receive a fair initial
hearing independent of their current score. Stable favorable reception reduces the need for another
generic vote; stable unfavorable reception also reduces repetitive voting without rejecting or
hiding the idea. A small positive exploration share keeps well-reviewed open ideas sampleable.

Exposure and uncertainty decide where more sampling could be useful; unanimity is not a stopping
requirement. Persistent disagreement redirects effort to a bounded challenge or comparison task
that seeks the reason for disagreement instead of asking for votes until one side wins. A reviewer
is never required to invent an objection in order to vote.

Initial voting is blinded to the current tally, rank and earlier evaluators' conclusions, while
retaining the subject's content and needed evidence. The review projection must not leak the
withheld fields; this is procedural blinding, not a claim to erase a model's prior knowledge. What
was shown and whether a review was blinded are recorded.

Review roles are reception, evidence checking, challenge, comparison, outcome checking, relevance
checking, filing and backlog. Open ideas are eligible for reception voting. Accepted proposals
leave generic voting and receive prerequisite, implementation and outcome attention instead. The
same budget funds every role; no role can create an unbounded recursive obligation.

The run's budget is the hard stopping condition. Policy supplies bounded per-item spending,
cooldowns, and a response to repeated skips, so unreachable items and persistent disagreement
cannot consume the whole allowance. Allocation across concurrent runs uses shared claims and
reservations with a fence, not a per-process copy of the budget. A claimed-but-interrupted
assignment does not count as a review and its reservation is reconciled.

Periodic coverage checks complement random selection: reserve initial-review attention for the
oldest due, eligible unreviewed artifacts, independently of their popularity. Exact weights,
targets, cooldowns, shares and ceilings are configurable values of a documented policy version,
recorded with each assignment so a selection can be replayed against its captured inputs.

## 6. Processing pipeline

### 6.1 Archive publication

The `archive` job backs this machine's session roots up with restic into one repository under the
machine's stable host identity, tagged `babel`, and writes back the one fact the catalog cannot
learn otherwise: which snapshot holds each session, and when.

One snapshot per root, not one for all of them: restic picks a parent by matching host and path
set, so a machine that gains a harness would, with one combined snapshot, find no parent and
re-read every byte. Per-root snapshots keep each root's parent chain stable, let one unreadable
root fail without taking the others with it, and make restoring one harness's sessions a restore of
one snapshot.

The repository and the secrets that open it come from the job's own service binding and nowhere
else. The job never creates a repository: a repository is created once, by hand, for the
deployment, because silent creation turns a mistyped locator into a second empty archive that grows
while the real one appears to stop, and two concurrent creations corrupt.

Captures are crash-consistent per file, not transactional across files. Session logs are
append-mostly, so a capture taken mid-write yields a prefix plus at most a torn final line; readers
tolerate that and the next snapshot supersedes it.

Retention is append-only. Nothing in Babel invokes `restic forget`, `prune`, `repair` or `unlock`,
and no path deletes a snapshot.

**Babel writes the archive and does not read it back.** Verifying and restoring are `restic check`
and `restic restore` run directly against the repository; `.omp/skills/babel-cli/SKILL.md` states
the procedure and `docs/parity.md` names the issue.

### 6.2 Catalog

The `scan` job discovers and describes this machine's sessions in place — title, workspace,
timestamps, repository fingerprint, completeness reasons, size, cost, turns, tool errors — and
writes one row per session. It reads the live files and never writes into them. The catalog is
rebuildable convenience state, never archive truth.

### 6.3 Ingest and normalize

The `prepare` job parses the selected sessions into one canonical record per line — object keys
ordered, insignificant whitespace gone — with an explicit opaque marker for a line that is not a
record, so nothing is ever dropped. It seals the result as the run's material, with an index naming
each session's selector, its file, and the digest it was served at. Unknown or partial Codex and
Claude structures degrade explicitly rather than being discarded.

### 6.4 Deterministic preflight

Before material reaches a model, likely secrets and high-risk data, malformed or truncated
sessions, transcript size, and duplicate or changed inputs are checked, and the result uses the
same evidence model as an observation. **Not built** (§3, `docs/parity.md`). Nothing scans a
preparation today, so the disclosure boundary is the operator's choice of Code profile.

### 6.5 Explore through Code

The operator names a preset — what the run is about — and picks a saved Code profile. Babel
composes the prompt from the recipes the policy holds, the answering protocol, the stage's schema
and instructions, and the material's own index, in that order, because a provider serves a
byte-identical prefix from its cache and the recipes are the largest invariant block. Code's
`runSession` door posts the session; Babel holds nothing.

The answer is the session's final message: the last fenced JSON block, validated against the
stage's schema. Every locator it cites must name a file the material's index served, at the digest
the index recorded. A retyped digest, an edited path, or a citation of a session this run was never
given refuses the whole answer.

**A refused submission is spend.** The model answered and the deployment paid; the receipt is
written with the cost and the refusal's own code, the claim is finished rather than abandoned, and
the tally counts the refusal by code — a drain that read a refusal as a free failure would relaunch
against a burn rate that never happened.

An accepted answer becomes records: a candidate is a hypothesis with its first status event, an
observation hangs off it carrying its locators, a finding consolidates observations, a proposal
addresses what it answers, and a question the corpus could not settle enters the ledger. Nothing is
written when the answer is refused, and a run never writes a ruling. Identifiers are minted from
the run and the model's own handle, so settling the same run twice writes the rows once.

### 6.6 Synthesize

Consolidation is part of the same answer: a run may consolidate the observations it developed
into a finding, name the observations it rests on, and address that finding with a proposal.
Consolidating across runs is the synthesize stage's job, and **only the explore stage runs**
(§5.4), so today a finding consolidates observations one run made.

### 6.7 Review and project

The operator records append-only rulings on any reviewable hypothesis, finding or proposal, with
an optional attributed reason. Babel preserves the complete evidence view independently of any
projection. Publishing, copying into an external system, or applying a proposed change happens
outside Babel.

### 6.8 Harness transformation in both directions

The pipeline is harness-agnostic end to end: original log → forward transformer → canonical model
→ reverse transformer → harness. Babel interfaces with the canonical model rather than with either
end. The adapters are the forward transformers; the canonical model is §4.1's source record, and
every later stage reads only that, so preparation, exploration, synthesis and review are written
once rather than once per harness.

OMP, Codex and Claude Code are the first implementations and not the closed set; Babel's own
analysis log is the fourth, and it writes OMP records because an envelope of Babel's design would
assert a schema over content Babel does not own. A harness is a name and the record language its
primary log is written in, declared in one place, and a harness whose language Babel already reads
joins by registering that pair.

The original bytes are retained verbatim, and they are what backup preserves. The forward
transformation is neither destructive nor authoritative: adapters read live files in place, restic
snapshots them as the harness wrote them, and the canonical model is recomputable by rescanning the
same bytes. That is why the parse is versioned — a better reading of an undocumented format is a
rescan, not a migration of stored derivatives.

Rehydration reads the retained original bytes and uses the canonical model only to locate records
within them. It never synthesizes a harness log from the canonical model alone, and that is a
requirement rather than a preference: the canonical model deliberately drops harness-specific
fields, envelope metadata and the identifiers a harness threads its own records with, so a
synthesized log would be unfaithful exactly where a resumed conversation depends on fidelity, and
silently. **Reverse transformers are specified and not built.**

## 7. Incremental behavior

When nobody pressed anything, the conductor decides what deserves a run. It wakes on the hub's
cadence, and every cycle is an ordinary run carrying an authority the receipt records.

A cycle draws against ceilings rather than a wish: a per-cycle cost, a daily cost, and a bound on
how many jobs one machine may hold at once. The bound is per machine, because a deployment-wide
number raised to admit a second machine's draws raises it for the first machine too. A claim is
serialized and reserves the cycle's ceiling before the run starts, so the day's limit binds on what
is committed rather than on what has already been spent.

A **budget** is a bounded, expiring exception to the standing policy, carrying what it moves, until
when, and why. A **drain** is a bounded burst the operator starts and stops, reported as it runs.

A cycle that does not spend says why: disabled, unrouted, nothing eligible, a ceiling reached, a
machine unavailable. Those reasons are counted and readable, because a loop that produced nothing
and said nothing is indistinguishable from one that is broken.

Every run records:

- the material it was served and the digest of each session in it;
- the focus and context subjects with their pinned fingerprints;
- the recipes and their versions;
- the Code profile and the model that answered;
- the prompt and schema versions;
- what it cost, in the hub's own meter and in the session's receipt; and
- its closure, and the refusal code where there was one.

Those inputs make a run reproducible enough to inspect, not deterministic enough to promise
identical ideas. Review decisions survive re-exploration; descendants and new evidence link to
rather than silently replace prior hypotheses or findings.

## 8. The surface

Babel is used through two panels. **Feed** is the front page and every record; **Watch** is what
Babel is doing, what it cost, and what it is set to do. There is no third surface, and no
capability lives only in a place the operator has to remember.

### 8.1 Doors, and what replaced the command line

Every act and every read is a door, named once in the plugin's contract. Reading: `feed`,
`record`, `thread`, `topics`, `topic`, `pulse`, `runs`, `run`, `policy`. The operator's acts:
`rule`, `comment`, `answer`, `interest`, `file`, `unfile`, `tell`, `setPolicy`, `setBudget`,
`clearBudget`. Running: `launch`, `stop`, `profiles`, `drainStart`, `drainStatus`, `drainStop`.
The crossing, owner only: `importLedger`, `rehostSessions`.

A door's result shape is spelled once, and a field a door does not spell is refused rather than
passed through. That is what keeps a panel from rendering something no contract promised.

### 8.3 Watch: runs, spend, and what Babel is set to do

Watch is the observatory: what is running now with its progress, what finished and what it cost,
the day's cycles and the reasons they did not spend, the machines and what they can run, the
standing policy with its ceilings and any budget overlay, the recipes the hub holds, and the
drains. It is where a run is started and stopped.

### 8.4 Stored data, stateless workers, and one interaction surface

Storage is the product. Everything Babel knows lives in the store (§9); a surface reads and
writes it, and a run is a stateless worker over it. **A capability that exists only as a
remembered command is unfinished.** Every stored thing is reachable and actionable in the panels:
reachable means reached by moving through the surface rather than by knowing a destination
exists, and actionable means the decisions a record admits are offered where that record is read
— a proposal's ruling beside the proposal, a question's answer beside the question. Both
requirements are load-bearing. A record only a URL reaches is not in the product, and a record
shown without the decision it invites sends the operator somewhere else to act on what he has
just finished reading.

### 8.5 Reading order and lifecycle views

The default order serves the operator's next useful decision: what needs him, by urgency, then by
kind — a proposal before a finding before a candidate at equal urgency — then by the longest
wait. The order is over the whole deployment before paging; a page ranked independently of the
set it came from is not an order. Every ordering names its basis, and missing evaluation data is
never rendered as zero opposition or unanimous support: a nought over nothing reads as a record
nobody objected to, which is the one defect this section exists to prevent.

Alternative orders are **new**, **top**, **controversial** and **rising** (§8.7), and filters over
the one list carry the rest: the record kinds, the topics, and the lifecycle states — open,
accepted, implemented, verified, deferred or rejected, with **reconsider** as a linked attention
view over earlier decisions. An **unreviewed** filter and a coverage summary expose
never-reviewed records, overdue initial reviews and reassessment due after changed context, by
review role, so the operator can see what has not been checked even when it has no votes.

A record shows its current revision, reception and assessment counts, evidence and unresolved
objections, and why it occupies this position. Bare votes stay bare rather than acquiring
generated rationales. Model reception and the operator's own choices are visibly different.

### 8.6 One record, peeled

A record is one page. It opens at its claim and expands in place, and nothing navigates away to
show more of the same object:

- **the claim** — one sentence, its standing, and the single act it invites;
- **the case** — the problem, the proposed outcome, how the operator would know it worked, what
  could go wrong, what is still unanswered. Prose, no identifiers;
- **the evidence** — quoted excerpts, each reaching its transcript at the cited line;
- **the reception** — who judged it, what they said, where they disagree; and
- **the machinery** — identifiers, digests, revisions, links, receipts, policy versions.

The first three depths carry no identifier at all, and the fifth is collapsed by default and never
mixed into them: a reader deciding whether a proposal is worth his time is doing a different job
from a reader debugging Babel.

**The operator's voice sits where he reads.** Three acts, none requiring a page change: an
attributed stance — agree, disagree, unsure — that decides nothing and says so; the append-only
ruling authority; and a reason in his own words, kept verbatim, attachable to either. The stance is
an operator-authored feedback record with explicit polarity, never an assessment, because an
assessment arriving through the operator surface would let a person mint what reads as a model's
observation. Babel's reception renders beside the operator's and is never summed with it.

**Density is a contract rather than a preference.** A listing row is one line of claim and at most
three facts; a page states one thing in one sentence; no page runs past roughly three screens
without pagination; badges are rationed to standing and kind, with coverage, lane, role and
reconsideration rendered as text or filter state; and one container vocabulary serves the whole
surface, so a different meaning gets a different shape rather than another class name with
identical rules.

### 8.7 The surface is a feed

**The front page is the feed, and there is one list.** Every record Babel has produced —
hypothesis, finding, proposal, and the questions it asks — is a post: one line of claim, its kind
as flair, the topics it belongs to, its age, Babel's score, its comment count, and, when it awaits
the operator, *why it is next* in five words. One sort bar over the whole deployment: **next** —
what needs the operator, a proposal before a finding before a candidate at equal urgency — plus
**hot**, **new**, **top** and **controversial** over an hour, day, week, month, year or all time,
and **rising**.

**Babel votes; the operator rules.** The score is Babel's reception and only Babel's: one vote per
run per role on one exact revision, support minus oppose, with the breakdown by role one gesture
away wherever the number appears. The operator does not vote, because his act on a record is a
ruling and a vote beside it would be a weaker copy of it. The acts a row offers are the rulings
themselves — accept, reject, defer, refine — and *ask*, which is a question to Babel about this
record, recorded as a comment Babel's next review of the record must answer.

**A topic is an entity.** A post's topics are the ledger entities it is filed under (§4.13); a
topic page is the feed narrowed to one entity, the rail lists entities with active lifecycle first,
and a record filed under nothing is in the feed as *unfiled* rather than hidden.

**Comments are the conversation under a post.** A reviewer's contribution prose, a refinement, the
operator's reason in his own words, his question, and the answer to a question are all comments,
threaded by what they relate to and shown newest-first under the record's depths. Rulings are the
moderator's log and render in the thread as the acts they are, attributed and dated, never as
comments. A run is the author of what it wrote: its name on a post or a vote reaches its run page.

**Navigation names decisions, not record kinds.** A destination named after a record kind or a
storage concept is a defect under this section. The kinds become filters over one list, which is
where a kind belongs: a distinction the reader applies when he wants it, not a place he has to go
first.


## 9. Durable state

**One store, on the hub.** Everything Babel knows is rows in one SQLite database the engine owns:
sessions, records, edges, statuses, rulings, filings, feedback, assessments, claims, entities,
facts, questions, answers, plans, steering, runs, progress, policies, budgets, drains and the
crossing's own ledger. Nothing is sealed and nothing is synced; a row is plaintext on the
operator's own server, and "published" is a word this schema does not need.

The shape is made by the enable hook, not by a migration: a fresh install runs the schema as one
batch when the file has no tables and records the shape's name for the next one to read. A
migration chain exists for a major bump over data that already exists.

**A batch is the transaction.** There is no open handle: read, decide, then a batch whose first
statements are its own guards, under the engine's bounds. A purge is the engine deleting the file.

**Every table that records an act is append-only by trigger.** A ruling, a vote, a filing, a
status, a record and a fact are written once and superseded by a later row, never edited or
deleted. The triggers are the whole of that guarantee. `actor_kind` is `operator`, `run` or
`engine` on every row that something wrote, so "who did this" is a column and never an inference.

**A run is a stateless worker over that storage.** It reads what is stored, writes back what it
concluded, and holds no authority and no durable state the storage does not already hold — which is
what lets a run be posted to any machine, interrupted, restarted or replaced without losing
anything that was not already written down. Which machine ran a cycle is a fact its receipt
records, not a place where knowledge accumulated.

Invariants:

- local source sessions are never modified;
- archived snapshots are never deleted by any path;
- every row carries a schema version and provenance;
- a record is never edited; a correction supersedes;
- a run may write records, edges, statuses and questions, and may not write a ruling;
- an assignment's claim is fenced, and a stale holder cannot settle it; and
- logs and errors never contain credentials, payloads or raw transcript bodies.

## 10. Quality and acceptance

Babel evaluates process quality and usefulness without claiming analytical reliability. A strong
developed hypothesis is interesting, inspectable, provenance-bearing, candid about uncertainty, and
economical of operator attention. A promoted finding should be specific, connected to supporting
and conflicting evidence, and clear about temporal limits; confidence never substitutes for
evidence.

Adapter fixtures are generated and synthetic, and deliberately exceed production: a fixture smaller
than production is a fixture that lets production break the reader. Generation is deterministic
from a seed and validated by having the real adapters read it. No real session data enters the
public repository or CI.

The plugin's own gate is the acceptance boundary: types, the test suite, a real pack of every
manifest, and a `verify` that composes the family on a disposable engine and asserts the store
exists after the doors have answered and is gone after a purge. It runs on every pull request and
every push to `main`.

A change to a surface is proved on the preview hub, by using it. Production is installed by the
operator, by hand.

## 11. Failure behavior

- A source changing during capture yields a crash-consistent file, never a claimed stable one;
  readers tolerate torn lines and the next snapshot supersedes it.
- An interrupted backup publishes no partial snapshot; a re-run uploads only chunks the repository
  does not already hold.
- A backup against a missing repository fails and says so rather than creating one; a repository
  that exists but does not open reports that instead, because initializing over it would answer a
  credential problem destructively.
- Unsupported or changed Codex and Claude formats preserve raw logs and mark metadata incomplete.
- A session that sealed no transcript submitted nothing: it settles at zero with the closure the
  job itself reported, and is counted as skipped rather than failed, because a streak of operator
  stops is not a broken lane.
- A refused answer is spend: the receipt carries the cost and the refusal's code, and nothing is
  written.
- A run Babel cannot read is retried once and then closed with that sentence and its claim
  released, rather than asked about on every wake for ever.
- A run whose material is no longer readable is refused rather than admitted: a result checked
  against nothing is an unverifiable claim recorded as a verified one.
- A hypothesis with missing or invalid provenance remains a visibly degraded candidate and cannot
  be promoted.
- Partial coverage is prominent; Babel never presents it as universal analysis.
- A local subject absent on the machine a run is scheduled on is refused rather than guessed, and a
  repository read leaves no trace inside the operator's checkout whatever the failure.
- No failure path falls back to deletion, whole-corpus download, unapproved provider selection,
  host mutation, or publication.

## 12. What is not built

This specification states what Babel is and, where a capability is designed and absent, says so at
the point it is described. [`docs/parity.md`](docs/parity.md) is the single list: one row per
capability the standalone product had, each present, absent by decision with the reason, or absent
with the issue that tracks it. A capability that is absent is absent in writing.

Two gates remain, and they are the operator's:

- **Before a capability that spends reaches a new machine**, its ceilings, its Code profile and its
  authorization are recorded policy on that hub. Saving a policy is not permission to launch
  compute.
- **Before anything runs on production**, it has been proved on the preview hub by using it.
  Production is installed by the operator, by hand, and nothing in this repository installs it.
