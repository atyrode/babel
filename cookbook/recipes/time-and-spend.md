---
id: time-and-spend
version: 1
kind: lens
scope: [session, corpus]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read, sandbox-exec]
default: false
---

# Time sinks and token spend

## Question

Where did the hours and the tokens actually go?

Every session in this corpus is a timed, metered record. Tool calls carry
durations; messages carry timestamps; usage metadata carries token counts and
cost. That makes a question answerable here that teams usually argue about
from impressions: what the operator's time and money are spent *on*, in
measured shares — building, waiting on CI, running tests, reconstructing
context, chasing a flake, re-reading what was already read, thinking, or
producing the deliverable itself. The value is not the totals, which the
dashboard already shows, but the *shapes*: a wait that recurs in every session
of one task class, a fixed cost paid at every session start, a loop that
burned an hour and appears in nine transcripts, a task class whose token
spend is out of all proportion to what it delivers. Each such shape prices a
possible improvement — and pricing is the point, because an improvement that
saves a recurring measured cost argues for itself, and one that saves an
imagined cost argues for nothing.

The lens measures **where recorded time and tokens went**, never how fast
anyone ought to have been.

## Inclusion, exclusion, and ambiguity

Include: recorded durations of builds, test suites, CI round trips, and other
waits, with their recurrence across sessions; gaps between a command
finishing and the work resuming, where the record shows the wait was the
bottleneck; session-opening spans spent re-establishing context that a prior
session held — the time-and-token price of a weak handoff; retry loops — the
same failing command repeated with small variations — with their measured
span; token spend per session joined against what the session delivered, at
the granularity of task classes rather than individual sessions; serialized
waiting where the record shows independent work that sat idle behind one slow
step; the fixed overhead every session pays before its first productive act.

Include the efficient cases deliberately: the session that parallelized its
waits, the task class whose cost fell after a tooling change, the expensive
run whose spend bought something durable. These are the material the
effective-patterns lens keeps, and they calibrate every claim about waste.

Exclude judgments about thinking time. Deliberation, reading, and exploratory
dead ends are the work, not overhead on it; a long pause before a good
decision is invisible to this lens by design, and only mechanical, repeated,
measurable waits qualify as sinks. Exclude tool pricing itself — what a token
costs is the operator's contract, not a finding. Test suite time priced
against test yield belongs to the test-economics lens; re-derivation caused
by an unread document belongs to document-ceremony; this lens owns the
general decomposition and should name the sibling lens when a sink's cause
lands in their ground. Exclude comparisons between people or agents; shares
are compared between task classes, tools, and time periods only.

Ambiguity: a long wall-clock span may be an operator away from the keyboard,
not a sink — wall time and working time diverge, and the record only
sometimes shows which. A retry loop may be honest debugging of a
nondeterministic fault. A high token spend may be the task's irreducible
size. When the record cannot separate these, the observation states both
readings; measured ambiguity is still worth more than confident impression.

Search the frontier before minting. Babel indexes its own hypotheses,
observations, findings, and the operator's recorded review answers; the job
document lists the prior records that looked related to this scope, and the
same search is available on demand through the corpus-search tool with
`"scope": "frontier"`. Everything it returns is a prior candidate idea and
not evidence. Where a prior record already says what yours would say, emit
against it by record id rather than minting a duplicate; where it is wrong,
contradict it with better arithmetic. Serendipitous scope makes those records
inspiration rather than a boundary; searching first still applies.

## Sorting cues

- the same command's duration recorded in many sessions — builds, suites,
  deploys — establishing a per-invocation price and an invocation count;
- watch loops and polling: repeated status checks against CI or a service,
  with the span from trigger to verdict;
- session openings dominated by re-reading, re-listing, and re-summarizing
  state a previous session already held;
- the same failing command run more than twice with variations, timed;
- a fast command run serially many times where the record shows independence;
- sessions of one task class whose token counts cluster far above the
  corpus's norm for comparable output;
- a fixed preamble cost visible at the start of every session of a class —
  environment setup, warmup, first-build penalties;
- explicit waiting language: "still running", "waiting for CI", "this takes a
  while", each attached to a measurable span.

Strongest cue: the same measured wait recurring across independent sessions
of one shape — recurrence converts an anecdote into a rate, and a rate into
a price. Retrieval rank is not a cue and never contributes to strength.

## Evidence and counter-evidence

Evidence, always measured, quoted with locators:

- durations from the transcripts' own run output, and timestamp gaps between
  recorded events — never estimates;
- invocation counts across the corpus for the recurring step, with the
  sessions named;
- token counts from usage metadata, aggregated by task class, with the
  aggregation stated;
- for context reconstruction, the opening span and what was re-established,
  beside the prior session that held it;
- for serialized waiting, the dependency structure the record shows —
  what actually needed what;
- a live re-measurement at the pinned snapshot (`sandbox-exec`) where the
  claim is present-tense — "this build takes four minutes" is checkable and
  should be checked.

Counter-evidence to seek:

- the wait already parallelized: work visibly proceeding during the span
  counted as idle;
- ambient load or environment variance inflating a recorded duration beyond
  what the step costs at the snapshot;
- the spend that bought a durable artifact — a cache, a fixture, a document —
  whose payout lands in later sessions' smaller numbers;
- the away-from-keyboard reading of a wall-clock gap;
- a since-made change that already removed the sink.

An observation here must be constructible from recorded numbers and located
spans alone. If it needs an assumption about attention, effort, or what
someone should have known, it is out of bounds.

## Temporal and present-reality checks

Costs drift with every tooling change: a build got a cache, a suite was
split, a flake was fixed, a model got cheaper. Date every rate to the window
it was measured in, and check the pinned snapshot before proposing —
re-measure the step (`sandbox-exec`) where the proposal's arithmetic depends
on the present price. Use `historical` for sinks a since-made change drained,
`still-applicable` where the rate persists, `resolved` where the record
shows the change landing and the rate falling, and `regressed` where a
drained sink is filling again — a build that got slow twice is a finding
about what keeps slowing it. Where neither the record nor a re-measurement
can establish the present rate, say `unverifiable`.

## Classifications and stopping conditions

- `recurring-wait` — a measured step price times a recorded invocation count,
  named to the step: build, suite, CI round trip, deploy;
- `context-reconstruction` — session-opening spans re-establishing held
  state, with the handoff boundary named;
- `retry-loop` — a measured span of repeated near-identical attempts, with
  the count and the exit;
- `serialized-independence` — measured idle behind a step the record shows
  nothing depended on;
- `fixed-overhead` — a per-session entry price, measured across the class;
- `spend-outlier-class` — a task class whose token cost per delivered result
  stands measurably apart, with the comparison stated;
- `efficient-shape` — the positive case: a wait parallelized, a cost that
  fell after a named change, an expensive run that bought a durable asset;
- `rate-unverifiable` — the honest class when spans or counts cannot be
  established from the record.

Stop when the rate is computed and shown — price, count, window, share — and
the smallest durable change is nameable with its arithmetic: a cache, a
scoped default, a parallel step, a warm fixture, a handoff artifact, a
cheaper model for a named class. Stop when counter-evidence converts the
sink into a purchase. Stop immediately if the next step requires judging how
long something *should* have taken; the lens prices what is, and the
operator prices what ought to be.

## Cross-session synthesis keys

Group by: the step — the same build, suite, or check across every session
that ran it; the task class, so token norms compare like with like; the
session phase — opening, loop, verification, delivery; and the wait's
downstream dependency, so that ten waits behind one slow step aggregate into
that step's price rather than ten anecdotes. Sum shares until they account
for the corpus's recorded time and spend at least roughly — a decomposition
that only itemizes the interesting parts and ignores the bulk is an
impression wearing numbers. State the window, and recompute rather than
extrapolate when the window moves.

## Capability needs

- `corpus-search` is required: recurrence, invocation counts, and task-class
  aggregation are cross-session arithmetic.
- `repo-read` at a pinned snapshot serves the causes: what a slow step does,
  whether the cache or parallelization already exists, what changed when a
  rate moved.
- `sandbox-exec` is used in the challenge stage to re-measure present-tense
  rates the proposal's arithmetic depends on. A run without it degrades
  those rates to `rate-unverifiable` rather than asserting them.

## Known failure modes

- **Pricing thinking.** The defining failure of this lens. Deliberation,
  reading, and dead ends are the work; only mechanical, repeated, measured
  waits are sinks, and an observation that reads as "too slow" about a
  person or an agent is invalid regardless of its numbers.
- **Impressionism with digits.** A number quoted without its window, count,
  and share is an anecdote. Every rate carries its arithmetic.
- **Micro-optimizing the rare.** A thirty-second step run monthly is not a
  finding. Recurrence and share gate everything.
- **Ignoring what the spend bought.** Durable artifacts, prevented failures,
  and later sessions' smaller numbers are the purchase side of the ledger;
  a lens that only sees costs proposes starving the things that pay.
- **Cross-agent league tables.** Shares compare steps, classes, and windows
  — never participants. The moment a comparison identifies who was slow, it
  is out of bounds.
- **Wall-clock literalism.** Gaps contain dinner. Say when the record cannot
  tell working time from elapsed time.
- **Re-minting what the frontier already holds.** Emit against the prior
  record; a duplicate is a duplicate an operator has to read.

## Examples

A useful observation: across fourteen sessions of one task class, the record
shows the same full rebuild at a measured median of six minutes, invoked
right after edits whose scope a partial build covers; the fourteen spans sum
to roughly an hour and a half in the window, and the build tool's own record
at the snapshot shows the partial target existing and unused. Evidence: the
fourteen timed invocations, located; the arithmetic; the snapshot's build
configuration. Classification `recurring-wait`, `still-applicable`. Smallest
durable change: the scoped default, priced against the measured rate.

A useful reconstruction observation: five consecutive sessions on one
feature each opened with eleven to nineteen minutes of re-reading the same
files and re-stating the same plan, measured from timestamps, before the
first new action; the prior session in each pair ended holding that state.
Classification `context-reconstruction`, with the handoff boundary named and
the five spans summed. The proposal is a handoff artifact, and its price is
the measured opening cost it would replace.

A useful spend observation: usage metadata over the window shows one task
class averaging four times the corpus's token norm per delivered change,
with the excess concentrated in repeated full-file re-reads visible in the
transcripts. The observation shows the distribution, not just the mean, and
proposes the narrower read pattern already used by the cheapest sessions in
the same class. Classification `spend-outlier-class`, `efficient-shape`
cited as the comparison.

A candidate correctly rejected: a two-hour gap between a failing run and its
fix, in a session whose timestamps place the gap across midnight with no
recorded activity. Wall clock, not work. `rate-unverifiable`, preserved with
its reason.

An error to avoid, stated plainly: "CI is eating our time" is not an
observation. The valid version is "the record shows twenty-two runs in the
window at a median twelve minutes trigger-to-verdict; in nine sessions the
next recorded action waited on the verdict; the nine waits sum to 1h48m" —
with all twenty-two locators and the arithmetic.
