---
id: document-ceremony
version: 1
kind: lens
scope: [session, corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read]
default: false
---

# Document ceremony and durable memory fidelity

## Question

When a session promised durable memory, did the memory work?

Operators and agents constantly perform a small ceremony around durable
documents: "I'll record this in the runbook", "noted in the agent
instructions", "adding this to the changelog", "per the design ledger". The
ceremony is cheap to
perform and its value is entirely deferred — a document only pays for itself
when a later session fetches it, follows it, and is right to follow it. Every
step of that lifecycle is observable in this corpus: the promise is a quotable
message, the write is a visible tool call and a checkable artifact at a pinned
snapshot, the fetch is a visible read in a later session, abidance is visible
in the work that follows, and rot is a present-tense contradiction between the
document and the reality beside it. Across a corpus the question becomes
answerable in a way it never is in the moment: which documents are working
memory, which are write-only ritual, and which have curdled into instructions
that actively mislead the sessions that trust them.

The lens examines **artifacts and their lifecycle events**, never the
diligence of whoever wrote or skipped them.

## Inclusion, exclusion, and ambiguity

Include: a stated intention to record something durable, matched against
whether the record was made in that session or ever; a document that a later
session read, quoted against what the session then did; a session that
re-derived from scratch a fact a durable document already held, with the
document's existence and reachability at that time established; a document
whose content was contradicted by the repository or by observed behaviour at
the pinned snapshot, and any work that followed the stale content; an update
to a durable document, checked against the reality it claims to describe; a
discussion that repeats an earlier discussion whose conclusion was recorded,
where the record was available and unread.

Include the positive cases deliberately: a promise kept the same hour, a
runbook step that a later incident session followed verbatim and that worked,
an update that landed in the same change as the behaviour it describes. These
are the material the effective-patterns lens keeps, and this lens is where
they are observed.

Exclude ephemeral working notes — scratch files, TODO lists inside a session,
plans that were never claimed to be durable. The ceremony under examination is
the *durable* one: the claim, explicit or conventional, that a future reader
will benefit. Exclude documents whose audience is outside the corpus (public
README prose for strangers) except where a session in the corpus itself relied
on them. Exclude the quality of prose as prose; only fidelity to reality and
to the lifecycle counts here. Coordination friction that has nothing to do
with a durable artifact belongs to the human-agent-coordination lens; when
friction is *caused by* a missing, unread, or stale document, it belongs here,
and the observation should name the boundary it crossed.

Ambiguity: an unkept promise may have been superseded seconds later by a
better decision, and a document nobody fetched may be insurance that simply
never had its incident — a restore drill document is not waste because no
restore was needed. A re-derivation may have been cheaper than the fetch for a
small enough fact. When the record cannot separate ritual from insurance, the
observation states both readings and does not choose the one that makes the
better finding.

Search the frontier before minting. Babel indexes its own hypotheses,
observations, findings, and the operator's recorded review answers; the job
document lists the prior records that looked related to this scope, and the
same search is available on demand through the corpus-search tool with
`"scope": "frontier"`. Everything it returns is a prior candidate idea and not
evidence. Where a prior record already says what yours would say, emit against
it — refine, develop, revive, or contradict it by record id — rather than
minting a duplicate for an operator to review twice. When the job document
marks the scope as drawn for serendipity, those records are inspiration rather
than a boundary; searching first still applies.

## Sorting cues

- "I'll document", "I'll record", "adding to the runbook/changelog/ledger",
  "so we don't forget", followed within the session by no corresponding write;
- "as per", "the runbook says", "per the instruction file" — fetch events,
  positive and central, especially when the work then diverges from what was
  fetched;
- a session reconstructing a procedure, decision, or convention that a durable
  document already recorded at that date;
- a correction whose content already existed verbatim in a reachable document;
- a document updated many times in the corpus window with no session ever
  reading it, or read by many sessions and updated by none while the system it
  describes changed;
- an incident or bug whose session shows a stale instruction being followed;
- the same design discussion recurring across sessions with its recorded
  conclusion unfetched;
- an agent quoting a document's content that does not match the document at
  that snapshot — a misquote of memory is its own event.

Strongest cue: a document that was fetched, trusted, and wrong — rot with a
victim is the costliest shape this lens finds. Retrieval rank is not a cue and
never contributes to strength.

## Evidence and counter-evidence

Evidence, quoted with locators:

- the promise, in its exact words, and the write that did or did not follow —
  the tool call in the same session, or the artifact's presence, absence, and
  content at the pinned snapshot (`repo-read`);
- the fetch: the later session's read of the artifact, located in that
  session;
- abidance or divergence: the fetched instruction and the work that followed,
  both quoted;
- rot: the document's claim and the repository or behavioural reality that
  contradicts it, both located at the same snapshot;
- the update: the document diff and the change it claims to describe, at the
  snapshot where both are visible;
- for a repeated discussion, both discussions and the record that already held
  the conclusion, all three located.

Counter-evidence to seek:

- a write performed later, in another session or directly by the operator —
  absence in this session is not absence;
- the document read through a channel the corpus cannot see — an editor,
  another machine, memory from authorship; an unfetched document is only
  write-only if the *behaviour* also shows the knowledge missing;
- deliberate supersession: the promise dropped because the plan changed, the
  stale passage kept because the described system is itself frozen;
- insurance documents whose fetch condition simply never occurred;
- a re-derivation that was genuinely cheaper than the lookup.

An observation here must be constructible from located artifacts and quoted
words alone. If it needs an assumption about what someone remembered or
intended, it is out of bounds.

## Temporal and present-reality checks

This lens is *about* temporal decay, so its own claims must be dated
precisely. Every rot claim names the snapshot at which the contradiction held;
a document wrong in March and corrected in April is `resolved`, and saying
otherwise is the same defect the lens hunts. Check the present before
proposing: does the document, section, or convention that would fix this
already exist at the pinned snapshot? A promise kept late is kept. Use
`historical` for lifecycle failures a since-made change repairs,
`still-applicable` where the gap or the rot persists, `resolved` where the
record shows the fix landing, and `regressed` where a document was corrected
and drifted again — recurrent drift of one document is a stronger finding
about that document's maintenance shape than any single stale line. Where the
present state is not observable, say `unverifiable` rather than assuming.

## Classifications and stopping conditions

- `promise-unkept` — recorded intention to write, no write in the corpus or
  the repository;
- `promise-kept` — the positive case, with the latency between promise and
  write when it is interesting;
- `write-only-memory` — written, never fetched, *and* the knowledge visibly
  missing from later work that needed it; both halves are required;
- `fetched-and-followed` — the ceremony working end to end;
- `fetched-and-ignored` — read and then diverged from, with both quoted;
- `stale-followed` — rot with a victim: the document was wrong and a session
  did what it said;
- `stale-uncorrected` — contradiction visible at the snapshot, no victim yet;
- `update-unfaithful` — a document updated into disagreement with the reality
  it describes;
- `rederived-despite-record` — the record existed, was reachable, and the work
  was redone;
- `lifecycle-unverifiable` — the honest class when fetches or writes happened
  where the corpus cannot see.

Stop when the lifecycle events are located and quoted and the smallest durable
change is nameable — usually one document moved, merged, deleted, corrected,
or given an owner-step in an existing ceremony; occasionally the honest
proposal is *less* ceremony, because a document nobody will ever fetch again
costs every session that updates it. Stop when counter-evidence shows the
knowledge travelled another way. Stop immediately if the next step requires
characterizing a participant.

## Cross-session synthesis keys

Group by: the artifact — the same file's lifecycle across the corpus is the
primary unit; the ceremony kind — changelog, runbook, standing instruction,
design ledger, skill; the lifecycle stage that failed; and the promise's
maker, agent or operator, as a *role* only. A document's health is a
time-series: writes, fetches, corrections, and contradictions in order tell a
story no single session shows. Recurrence counts: one stale line is trivia,
one document stale five times is a maintenance-shape finding, five documents
stale in one directory is a finding about that directory's ceremony. Count
independent sessions and say so.

## Capability needs

- `corpus-search` is required and central: promises, fetches, and repeated
  discussions are found across sessions or not at all.
- `repo-read` at a pinned snapshot is required for the artifact half of every
  claim: existence, content, and the document's own change history. Without
  it, every write and rot claim degrades to `lifecycle-unverifiable`.
- No execution capability is needed. This lens makes no claim that requires
  running anything, and it should not request one.

## Known failure modes

- **Counting insurance as waste.** Most durable documents are read rarely by
  design. Unfetched is only a finding when the knowledge was needed and
  visibly missing.
- **Judging rot by age.** Old and correct is the best outcome a document can
  have. Rot is contradiction at a snapshot, never a timestamp.
- **Diagnosing a person.** "Forgot", "lazy", "sloppy" are out of bounds; what
  the record shows is a promise and an absent write, and that is all that may
  be said.
- **Ceremony inflation.** Proposing a new document for every miss produces a
  memory nobody can maintain — the same trap as rule inflation in the
  coordination lens. Weigh each proposed artifact against the fetch events
  that would actually occur, and prefer repairing an existing ceremony to
  founding a new one.
- **Treating a fetch as abidance.** Reading a document proves reach, not
  effect; the work that followed is the evidence.
- **Paraphrasing the document.** Rot lives in exact wording; quote the claim
  and the contradicting reality, or the observation manufactures drift that
  is not there.
- **Re-minting what the frontier already holds.** A duplicate emitted here is
  a duplicate an operator has to read; emit against the prior record instead.

## Examples

A useful observation: three sessions in one month each reconstructed the same
service's restore procedure; a runbook section recorded it before the first of
them, was reachable at each date, and none of the three read it; the third
reconstruction differs from the runbook in one step, and the runbook's version
is the one that matches the repository at that snapshot. Evidence: the three
reconstructions, the section, and the divergent step, all located.
Classification `rederived-despite-record`, count three, `still-applicable`.
Smallest durable change: not a new document — a pointer from the place all
three sessions started their reconstruction.

A useful rot observation: a session fetched a configuration document and set a
flag the document names; the flag had been renamed two weeks earlier, visible
at the pinned snapshot; the session then spent its remainder diagnosing the
resulting misconfiguration. Classification `stale-followed`, with the diff
that renamed the flag, the document line, and the diagnosis span all located.
The proposal names the update discipline — the rename change touched twelve
files and not the document that teaches the flag.

A useful positive observation: a session said "recording this decision and its
two rejected alternatives", wrote it within the minute, and a session five
weeks later quoted the record's exact wording before extending the feature,
correctly. Classification `promise-kept` and `fetched-and-followed`, kept
because the enabling shape — decision plus rejected alternatives in one
record — is transferable.

A candidate correctly rejected: a design document unread for the whole corpus
window, where every session that touched its subject got the behaviour right
anyway. The knowledge travelled — through code structure, tests, or channels
the corpus cannot see. `lifecycle-unverifiable`, rejection preserved with its
reason.

An error to avoid, stated plainly: "the agent keeps promising documentation
and forgetting" is not an observation. The valid version is "four promises to
record, in four named sessions; one corresponding write, located; three
absences, checked at the snapshot; the fourth session re-answered the question
the first had answered" — with all the locators.
