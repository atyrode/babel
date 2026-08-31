---
id: test-economics
version: 1
kind: lens
scope: [session, corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read, sandbox-exec]
default: false
---

# Test economics: what the tests defend and what they cost

## Question

Did the tests written in these sessions earn their keep?

A test is a purchase: it costs writing time once and running time forever, and
it pays out only when it fails for a real reason — a defect caught before
delivery, a regression caught after, a contract made legible to a later
reader. This corpus records both sides of the ledger in unusual detail. The
purchase is visible: the session that wrote the test, what it asserts, and
whether writing it was asked for. The payouts are visible: a later run where
that test fails and the *source* is then fixed. And the recurring costs are
visible too: suite durations printed in transcripts, reruns chasing a flake,
sessions spent editing tests that broke against a refactor while the
behaviour they claimed to defend never changed. Across sessions the question
sharpens from "are there tests" — which any coverage tool answers — to the
question coverage tools cannot answer: which tests are working assets, which
are ballast maintained out of politeness, and which assert so little that
they pass precisely when they are needed most.

The lens examines **test artifacts and their recorded life**, never the
competence of whoever wrote them.

## Inclusion, exclusion, and ambiguity

Include: a test written in a session, read against the contract it claims to
defend — does it pin behaviour an operator could observe, or restate the
implementation beside it; a test failure event and what changed next, the
source or the test; a flake — the same test passing and failing with no
relevant change between runs — and the time its chases consumed; suite
duration as recorded in transcripts, multiplied by how often the record shows
the suite running; a test deleted, skipped, or loosened, with the reason the
record gives; a defect that shipped through a green suite, examined for what
the suite asserted at that snapshot; a test a later session read as
documentation of the contract, and whether what it taught was right.

Include the positive cases deliberately: the test that failed during
development and visibly redirected the change before delivery; the regression
a test caught that a session then traced to a real cause; a suite whose
runtime stayed proportionate as the corpus window advanced. These are the
material the effective-patterns lens keeps.

Exclude coverage percentages and test counts as findings in themselves — the
lens is about what tests *do*, not how many exist. Exclude test style and
naming except where they change what is defended. Exclude one-off experiment
scripts never claimed to be durable defense; those belong to ordinary work.
Whether a *promised* test was ever written belongs to the document-ceremony
lens's promise machinery; here the artifact exists and the question is its
economics. General time accounting beyond tests — builds, CI waits, thinking —
belongs to the time-and-spend lens; suite time is included here because it is
priced against the same tests' yield.

Ambiguity: a test that never fired may be a deterrent whose value the record
cannot show — absence of payout is not proof of waste, and the honest
observation says so. A flake may be the defect itself: a test honestly
reporting a real race loses its "flaky" label the moment the race is found.
A test edited after a failure may be legitimate contract evolution rather
than brittleness, distinguishable only by whether the *intended* behaviour
changed. When the record cannot separate these, the observation states both
readings and does not choose the one that makes the better finding.

Search the frontier before minting. Babel indexes its own hypotheses,
observations, findings, and the operator's recorded review answers; the job
document lists the prior records that looked related to this scope, and the
same search is available on demand through the corpus-search tool with
`"scope": "frontier"`. Everything it returns is a prior candidate idea and not
evidence. Where a prior record already says what yours would say, emit against
it by record id rather than minting a duplicate; where it is wrong, contradict
it explicitly. Serendipitous scope makes those records inspiration rather than
a boundary; searching first still applies.

## Sorting cues

- a test failure followed by an edit to the test and no edit to any source —
  the strongest brittleness cue, especially recurring across refactors;
- a test failure followed by a source fix — the payout event, worth locating
  precisely and keeping;
- rerun-until-green sequences: the same suite invoked repeatedly in one
  session with no intervening change;
- suite invocations whose recorded duration dominates the session's span;
- assertions that mirror the implementation line for line, assert on private
  plumbing, or pin incidental output — tests that cannot fail for a reason an
  operator cares about;
- `skip`, `todo`, or commented-out tests accumulating at the snapshot;
- a bug session whose defect sat inside behaviour a green test claimed to
  cover;
- a session reading a test file to learn how a feature behaves — yield that
  never appears in any failure count;
- tests written unprompted alongside work whose instructions asked for none,
  and tests explicitly requested that never appear — both directions matter.

Retrieval rank is not a cue and never contributes to strength.

## Evidence and counter-evidence

Evidence, quoted with locators:

- the test's assertions at the pinned snapshot (`repo-read`), against the
  contract stated in the session that wrote it;
- failure events: the run output in the transcript, and the diff that
  followed — source or test — located;
- flake evidence: the passing and failing runs, timestamps, and the absence
  of relevant change between them; a live re-run at the pinned snapshot
  (`sandbox-exec`) where the claim needs present-tense proof;
- cost evidence: recorded durations and invocation counts, from the
  transcripts' own run output — measured, never estimated;
- for a shipped defect, the suite's actual assertions at that snapshot beside
  the defect's observable behaviour;
- deletion or loosening diffs, with the stated reason quoted.

Counter-evidence to seek:

- the regression the test caught silently: a failure that redirected work
  mid-session without ceremony, easy to miss and full payout nonetheless;
- contract evolution behind an edited test — the requirement changed, so the
  edit is maintenance of a living asset, not brittleness;
- the flake's root cause being real concurrency, which converts the cost into
  the discovery;
- a slow suite guarding something whose failure cost dwarfs its runtime,
  which the record may show in an incident it prevented elsewhere;
- a deleted test whose feature was deleted with it.

An observation here must be constructible from located runs, diffs, and
quoted assertions alone. If it needs an assumption about diligence or intent,
it is out of bounds.

## Temporal and present-reality checks

Test economics drift fast: a brittle test may have been rewritten, a flake
fixed, a slow suite split since the sessions under analysis ran. Check the
pinned snapshot before proposing — the test's current assertions, whether it
still exists, what the suite now costs (`sandbox-exec` where measuring is the
only honest answer). Use `historical` for costs a since-made change removed,
`still-applicable` where the shape persists, `resolved` where the record
shows the fix landing and the chase stopping, and `regressed` where a
stabilized test is flaking again. A payout is never historical: a test that
caught a real defect once has already earned something no later rewrite
removes. Where runs cannot be re-verified and the transcript is silent, say
`unverifiable`.

## Classifications and stopping conditions

- `defect-caught` — failed during development, source changed in response;
  the primary payout, kept deliberately;
- `regression-caught` — failed after delivery against a real cause;
- `brittle-maintenance` — failures answered by test edits across refactors,
  with a count and no behavioural change defended;
- `flake` — inconsistent verdicts without relevant change, with the rerun
  count and time consumed; reclassified the moment a real race is found;
- `ceremony-assertion` — a test that cannot fail for an operator-visible
  reason: mirrors implementation, asserts plumbing, or tautology;
- `undefended-contract` — a shipped defect inside behaviour the suite claimed
  green, with what the assertions actually pinned;
- `suite-drag` — measured runtime × recorded frequency out of proportion to
  the suite's recorded payouts;
- `test-as-documentation` — read by a later session to learn the contract,
  and right;
- `economical-defense` — the positive case: cheap, stable, and it fired;
- `yield-unverifiable` — the honest class when the record cannot show
  whether a test ever mattered.

Stop when the ledger entry is quoted and located — the assertion, the
failure, what changed next, the measured cost — and the smallest durable
change is nameable: one assertion rewritten against the contract, one flake
quarantined with its race named, one suite split, one deletion proposed *with
its contract shown undefended by anything else*. Stop when counter-evidence
converts cost into payout. Stop immediately if the next step requires
characterizing a participant.

## Cross-session synthesis keys

Group by: the test file and the individual test — one test's life across
sessions is the primary unit; the failure-response shape (source fixed vs
test edited); the suite invoked; and the contract defended, so that five
brittle tests pinning one interface aggregate into a finding about that
interface's testability rather than five trivia. Sum measured costs across
sessions before judging proportion — a ninety-second suite is trivia once and
a finding at forty recorded invocations. Count payout events per suite and
say when the count is zero *and* when it is merely invisible.

## Capability needs

- `corpus-search` is required: failure events, reruns, and reading-as-
  documentation are cross-session facts.
- `repo-read` at a pinned snapshot is required for what a test asserts, its
  edit history, and the failure-response diffs. Without it, brittleness and
  ceremony claims degrade to `yield-unverifiable`.
- `sandbox-exec` is used sparingly, in the challenge stage above all: re-run
  a suite to verify a duration claim, re-run a flake candidate at the pinned
  snapshot for a present-tense verdict. Claims that rest on a live run say
  so; a lens run without this capability degrades those claims to
  `unverifiable` rather than asserting them.

## Known failure modes

- **Survivor accounting.** Counting only fires and calling everything else
  waste. Deterrence and documentation are real yield the failure count never
  shows; the honest classes exist to hold them.
- **Coverage worship, inverted or upright.** Neither "coverage rose" nor
  "coverage is vanity" is an observation. Only what a test defends and what
  it cost may be said.
- **Hindsight prosecution.** After any bug, some test was "missing". An
  `undefended-contract` finding requires showing the suite *claimed* that
  ground — a green run whose assertions are quoted — not that more tests
  would have helped, which is always true and never useful.
- **Diagnosing a person.** "Wrote lazy tests" is out of bounds. The record
  shows assertions, failures, and diffs; that is all that may be said.
- **Deletion from cost alone.** Proposing to remove a test because it is slow
  or brittle, without showing its contract defended elsewhere, trades a
  measured small cost for an unmeasured large one.
- **Counting exploration as suite cost.** A developer running one test twenty
  times while building is the tool working, not drag.
- **Re-minting what the frontier already holds.** Emit against the prior
  record instead; a duplicate is a duplicate an operator has to read.

## Examples

A useful observation: one test failed in eleven sessions across the corpus
window; all eleven resolutions edited the test's expected output and none
touched the source; the assertions pin a rendered table's exact column widths
while the sessions' own instructions describe the contract as "the rows
appear". Evidence: the eleven failure-and-diff pairs, located, and the
assertion block quoted. Classification `brittle-maintenance`, count eleven,
`still-applicable` at the snapshot. Smallest durable change: assert row
presence and ordering, not geometry — the contract the instructions actually
state.

A useful payout observation: a session's suite run failed on a boundary case
test written four months earlier; the session traced it to an off-by-one in
new pagination code and fixed the source; the test was unchanged. Evidence:
the failing run, the fix diff, the test's authorship session. Classification
`regression-caught`, kept because it prices the suite's value with a real
event rather than sentiment.

A useful cost observation: transcripts record the full suite at roughly nine
minutes, invoked on average five times per working session across two months
— measured, with invocation counts — while every recorded payout in the
window came from two packages that run in forty seconds. The observation
proposes running those two packages in the loop and the full suite at
boundaries, and states plainly that unmeasured deterrence may justify more.
Classification `suite-drag`, with the arithmetic shown.

A candidate correctly rejected: a test that reruns green three times in one
session after two red runs — but the intervening diff touched the code under
test. Not a flake; ordinary iteration. The rejection is preserved with its
reason.

An error to avoid, stated plainly: "the tests are mostly useless ceremony" is
not an observation. The valid version is "of the suite's nineteen recorded
failure events, sixteen were resolved by editing expected values, two by
source fixes, one is unresolved; the sixteen span four refactors of one
interface" — with the locators for all nineteen.
