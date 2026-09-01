---
id: security-trust-boundaries
version: 3
kind: lens
scope: [session, corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read, sandbox-exec, public-research]
default: true
---

# Security, privacy, and trust boundaries

## Question

Where did authority, secrets, or private data cross a boundary that nobody
drew on purpose?

Coding sessions are unusually rich evidence for this question because they
record the moment a boundary was decided, and usually the reasoning too. A
transcript shows a credential being pasted, a token being echoed into a log, a
permission being widened to make a command work, an untrusted input being
interpolated into a shell string, or a destructive command being run against
something that was not disposable. Repository state shows the boundary that
survived. The gap between the two is where exposure lives.

The question is fruitful for a second reason: boundaries erode gradually and
locally. Each individual widening was reasonable in its moment, and nothing in
a single session reveals the accumulation. A corpus does.

## Inclusion, exclusion, and ambiguity

Include concrete, locatable exposure and concrete missing containment:
credential material in a place that persists, authority granted more broadly
than the task needed, untrusted content reaching an interpreter, a destructive
operation without a scoped target, a boundary that a change removed, private
data written to a location whose readers are wider than its subject.

Include the containment that is absent: no validation at a trust boundary, no
scoping on a token, no confirmation on a destructive path, no separation
between the process that holds a secret and the process that runs untrusted
code.

Exclude generic advice. "Consider rotating credentials regularly" is not an
observation; "this credential value appears in a file the record shows being
committed" is. Exclude severity theatre: an observation states the concrete
consequence it can support, and if it cannot describe a consequence it is not
ready. Exclude anything requiring exploitation to demonstrate — the sandbox
tests behaviour, never a live target, and nothing in this lens authorizes
reaching a real system.

Exclude secret *values* from the observation text itself. Evidence is the
locator plus the shape of the finding; the value stays where it is. A likely
secret detected by preflight is a locator and a classification, not a payload
to quote.

Ambiguity is the normal case. A high-entropy string may be a test fixture, an
example, or a live credential, and the record often cannot distinguish them.
State which it is when the evidence supports it, and state the ambiguity when
it does not: "a value shaped like a credential, in a file whose other values
are synthetic" is an honest observation. A permission that looks excessive may
be the minimum the platform offers. A boundary that looks missing may be
enforced one layer out, in code the corpus does not contain.

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

- a secret-shaped value in a location that persists — committed file, log,
  shell history, issue text, or transcript that leaves the machine;
- a credential or token used by more than one component, or with no recorded
  scope limit;
- untrusted input — transcript text, issue text, web content, tool output —
  reaching a shell, a query, a template, a deserializer, or a path;
- authority widened to unblock a task: a permission bit, a broader mount, a
  disabled check, an added exception;
- a destructive command whose target is computed rather than named;
- removal of a check, a confirmation, or an isolation step;
- a boundary crossing between a process holding secrets and a process running
  untrusted code;
- private data — paths, names, internal hostnames, personal data — flowing
  into a public-facing artifact.

Strong signal: a widening that was explicitly temporary and is still present
at the pinned snapshot. Weak signal on its own: the presence of the words
"secret", "token", or "key". Retrieval rank contributes nothing to severity.

## Evidence and counter-evidence

Evidence, always with locators:

- the exact record showing the value, grant, command, or removal;
- repository state at the pinned snapshot showing whether it persists;
- the reachability path: what untrusted input reaches the sink, and through
  which recorded step;
- deterministic preflight results, which use the same evidence model and are
  frequently the strongest available signal for likely secrets;
- an experiment in a disposable clone demonstrating that a boundary behaves as
  described — for example, that an input is not validated — without touching
  anything real;
- authorized public research establishing that a construct is known-unsafe,
  when the run grants it, as background rather than as evidence about this
  corpus.

Counter-evidence to seek deliberately:

- the value is synthetic, expired, revoked, or a documented example;
- the file is ignored, generated, or never published;
- validation exists one layer out;
- the grant is the platform's minimum;
- the destructive path is scoped by something outside the visible record;
- the exposure was remediated later, in this or another session.

An exposure observation without an attempt at each of these is incomplete.
Rotation and remediation are outside Babel entirely, so an observation that
overstates a closed exposure costs real, misdirected operator effort.

## Temporal and present-reality checks

Time matters more here than in any other lens, because a past exposure is not
undone by a present fix: a credential that was exposed remains exposed until
it is rotated, and rotation is not visible in a repository snapshot.

So keep two statuses apart. **Present state** is what the pinned snapshot
shows: whether the value, grant, or missing check is still there. **Historical
exposure** is what the record shows happened, and it stays true regardless of
present state. A committed credential that is deleted at the snapshot is
`resolved` as to present state and still a historical exposure whose
consequence depends on rotation the corpus cannot observe — record that as
`unverifiable` and say what evidence would settle it.

`regressed` deserves specific attention: a boundary restored and then removed
again is a stronger finding than either event alone.

## Classifications and stopping conditions

- `credential-exposure` — secret material in a persisting location;
- `over-broad-authority` — more permission than the recorded task needed;
- `untrusted-input-reaching-sink` — a reachability path exists;
- `destructive-operation-unscoped` — irreversible action, computed target;
- `containment-missing` — no isolation between secret-holding and
  untrusted-code-running components;
- `private-data-in-public-artifact` — disclosure class violated by a flow the
  record shows;
- `boundary-removed` — a check, confirmation, or isolation step deleted;
- `hardening-opportunity` — no demonstrable exposure, a boundary worth
  strengthening. This is the honest home for most candidates and must not be
  dressed up as the classes above.

Stop when the reachability path is either established with locators or shown
to be broken; when the counter-evidence list is exhausted; or when the next
step would require touching a real system, a live credential, or an
exploitation attempt — at which point the observation records what is known and
what would settle it. Also stop when the candidate has become a code-quality
concern with no security consequence, and let the code-health lens have it.

Sensitivity is part of the output: findings here default to the private
security-brief destination, and nothing in this lens proposes a public
artifact containing a locator, a path, or a value.

**Claims and remedies.** A hypothesis states what is the case at a boundary; a
remedy states what should change about it. The optional per-candidate `remedy`
is a want or an option, never a verified fact and never a finding, and it
carries no evidence weight of its own — its backing is the claim it addresses,
and naming a control has never been evidence that a control is missing.
Candidate-status framing and the sanitization rules above hold over a remedy
exactly as they hold over the observation: no value, and a remedy that names a
control inherits the claim's sensitivity and its private destination. The
dispositions are independent. An operator who declines a proposed control has
not disputed the crossing, and accepting that a crossing happened authorizes
nothing about which control should exist.

A candidate carrying both emits both records, joined by an `addresses` edge,
which is what keeps a rejected control from taking a real exposure down with
it. Competing remedies are expected here and must stay separate: scoping the
token, moving the secret out of the argument list, and splitting the process
that holds it from the process running untrusted code are three different costs
with three different blast radii, and one hedged "improve credential handling"
is reviewable as none of them. One remedy may also address several claims — a
single isolation step often answers every crossing between the same two
components. The honest default remains a claim alone, most of all under
`hardening-opportunity`, where the boundary is worth strengthening and the
record does not say how; a remedy invented to make the candidate feel
actionable converts a defensible structural observation into an unwanted
suggestion the operator must now decline.

Concretely: "the record shows a process holding a provider credential also
running an untrusted repository's build, and the snapshot shows no separation"
is the claim, with the record and the snapshot as its locators. "Run untrusted
builds in a credential-free child process" is a remedy addressing it. The
crossing is a fact about the record whether or not that control is ever
adopted, and rejecting the control leaves `containment-missing` standing
exactly as evidenced.

## Cross-session synthesis keys

Group by: boundary type; the component or entity holding the authority; the
credential or data class, never its value; the sink; and the mechanism of the
widening.

Recurrence is available to every lens and is particularly load-bearing here,
because the interesting unit is often not one exposure but one habit: the same
widening mechanism appearing in unrelated work, or the same class of value
reaching the same class of sink. A finding states how many independent
sessions support it, and whether the exposures share a component, a cause, or
only a resemblance.

## Capability needs

- `corpus-search` is required.
- `repo-read` at a pinned snapshot is what separates present state from
  historical exposure. Without it, every observation is historical only and
  must say so.
- `sandbox-exec` can demonstrate that a boundary behaves as described, inside a
  disposable clone with no network and no host authority. It never validates
  against anything real.
- `public-research` is optional background: whether a construct is
  known-unsafe. It is brokered, and no private locator, path, value, or
  identifier may appear in a request. Research is never evidence about this
  corpus.

## Known failure modes

- **Quoting the secret.** The single worst failure available here: an
  observation that copies a live value into a durable record widens the
  exposure it reports.
- **Severity inflation.** Reporting a hardening opportunity as an exposure
  destroys the operator's ability to triage the next one.
- **Reachability assumed.** A dangerous sink and untrusted input in the same
  repository are not a path. The path is evidence.
- **Fixture blindness.** Synthetic corpora and test fixtures are full of
  credential-shaped values by design.
- **Present state read as history.** A deleted secret is not an unexposed
  secret.
- **Scope creep into incident response.** Babel does not rotate, revoke, or
  respond; an observation that assumes it does is proposing work in the wrong
  system.
- **Compliance as correctness.** A complete, well-classified observation can
  still be wrong about the boundary.
- **Re-minting what the frontier already holds.** One idea recorded four times
  across four runs, each copy carrying its own review history and nothing in any
  of them saying it is the fourth. Babel warns when a new candidate's statement
  closely overlaps an existing one, and records that warning on the candidate
  rather than dropping it — so a duplicate emitted here is a duplicate an
  operator has to read.

## Examples

A useful observation: a record shows a token being passed as a command-line
argument to unblock a failing step; the pinned snapshot shows the same
invocation in a committed script; the token's shape matches a live credential
class and preflight flagged it. Evidence: the record, the committed line, the
preflight result — locators only, no value. Counter-evidence sought: whether
the value is synthetic (its neighbours are not), whether the file is generated
(it is not), whether it was later removed (it was not). Classification
`credential-exposure`, present state `still-applicable`, historical exposure
`unverifiable` as to rotation. Sensitivity: private security brief.

A useful containment observation with no exposure: a record shows a process
that holds a provider credential also running an untrusted repository's build.
No exposure is demonstrated. Classification `containment-missing`, with the
concrete consequence stated as a capability, not an incident. This is worth
review precisely because it is structural.

A candidate correctly rejected: a high-entropy string in a synthetic fixture
generated by the project's own corpus generator, where the generator's code is
in the snapshot and produces exactly that shape. The counter-evidence is
conclusive, and the rejection is preserved so the same string does not become a
finding next month.

An error to avoid: concluding that an over-broad permission is an exposure
because a widely-known guideline says so. The guideline is background; the
observation needs the recorded grant, the task it unblocked, and what the
present snapshot still allows.
