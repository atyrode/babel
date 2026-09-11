# Full-lifecycle evaluation: implementation plan

**Status: approved design; runtime implementation is not delivered by this PR.**
The work below is tracked in [#219](https://github.com/atyrode/babel/issues/219).
A documentation PR references that issue; it does not close runtime acceptance.
No acceptance scenario below has been exercised as part of this design delivery.

The authoritative contracts are SPEC
[§4.12](../SPEC.md#412-evaluation-reception-and-observed-outcomes),
[§5.8](../SPEC.md#58-backlog-first-evaluation-within-a-budget),
[§8.5](../SPEC.md#85-evaluation-inbox-and-lifecycle-views), decision 89, and the
[activation gate](../SPEC.md#before-full-lifecycle-evaluation-is-activated).
This document owns implementation order and evidence tracking, not a second
product specification. Baseline references below describe
`578df6653907dc8dce49b8901de1cec9303a7c91` (`578df66`, v0.2.5).

## 1. Decisions to preserve

- A bare support, opposition, or uncertainty vote is a valid reception review.
  Comments, new evidence, and refinement are optional. Skips and failures are not
  votes. Reception is not evidence strength, independent corroboration, or a
  probability that the idea is correct.
- Coverage includes hypotheses, proposals, and other reviewable Babel output,
  including observations and findings. Review roles distinguish reception from
  evidence checking and outcome verification. Missing evaluators are gaps, not
  a reason to call something reviewed or automatically not applicable.
- The **operator queue** recommends useful next decisions based on recorded
  current work, pain, Reality context, actionability, and reception. The **worker
  queue** spends attention where review is still useful. Neither accepts,
  rejects, defers, merges, or shelves an item on the operator's behalf.
- Authorized evaluation work is backlog-first, with protected discovery and
  random exploration. Periodic coverage reserves attention for oldest-due
  initial reviews; weighted randomness allocates further review. No budget is
  increased or compute launched merely because a backlog exists.
- Full lifecycle is in scope. Babel may record evidence-backed implementation
  and outcome assessments; changed evidence on decided work creates Reconsider
  items without reversing the decision. Hypotheses retain their own lifecycle.
- Alternatives can be compared together without merging their records. Explain
  why now, remaining objections, and what could change the recommendation where
  known. Preserve explicit feedback reasons; clicks and silence are not intent.

## 2. Existing seams and gaps

| Area | Baseline seam | What the new work must add |
| --- | --- | --- |
| Immutable analysis records and revisions | `internal/frontier/model.go`, `store.go`, `revision.go` | Revision-bound evaluation subjects without changing existing evidence minima |
| Operator decisions | `internal/review/service.go:34-88,155-208,395-470` | Keep disposition authority separate; coverage must not depend only on review-queue enrollment |
| Proposal triage | `internal/frontier/triage.go:45-80,147-188,277-330,418-481` | Existing advice requires a counterargument, ranks within a cohort, targets unruled proposals, and exposes a presence marker; it is not the new reception system |
| Durable publication and remote decoding | `internal/frontier/publish.go:59-94,131-273`, `remote.go:98-197,345-443` | Stage and publish every new record family and reconstruct it on a non-producing instance |
| Catalog kinds and confidentiality | `internal/sharedcatalog/sync.go:52-73`, `migrations/0003_phase_b_records.sql:80-100`, `allowlist.go:315-346` | Add compatible kind/schema support without widening plaintext eligibility for content-derived judgments |
| Worker results and replay | `internal/explore/tools.go:21-72`, `schema.go:30-107`, `records.go`, `ledger.go:31-55,197-218` | Review assignments, blinded read context, validated evaluation results, and idempotent persistence |
| Conductor allocation | `internal/conductor/ladder.go`, `duty.go`, `budget.go:19-96`, `conductor.go:91-136` | Coverage, weighted review selection, recorded random inputs, and a hard budget across participating workers; existing receipt-derived local accounting alone is insufficient for a fleet-wide allowance |
| Relevant Reality context | `internal/reality/attention.go:22-70,107-175` | Reuse recorded work allowances and provenance; do not replace them with an inferred-interest policy |
| Rebuildable reads | `internal/index/frontier.go:36-46,104-152`, `internal/web/fleet.go:335-470` | Bounded ranking/coverage projections over deployment-wide input, not whole-corpus payload opens per page |
| Browser | `internal/web/analysis.go:809-844,960-1024`, `review.go:32-59`, `web/src/pages/ReviewPage.tsx`, `web/src/triage.tsx` | Coverage, sorts, lifecycle assessments, comparisons, and decision controls through the same Go services |

The active `cookbook/recipes/babel-triages-the-queue.md` remains **version 1**
in this documentation PR. Its mandatory-counterargument, proposal-only contract
is an implementation baseline superseded in product intent by SPEC §4.12. Do not
change its prompt to emit votes before the writer can accept them. Existing
proposal/detail navigation is not proof of complete evaluation coverage, and
un-enrolled records must not disappear from the new coverage inventory.

## 3. Integration sequence

All stages are **not implemented**. Record a delivered revision and observed
acceptance here as stages land; intermediate code does not enable the full policy.
Stages describe dependency boundaries, not separate reduced-scope products.

### E1 — Records, revision binding, and review applicability

Extend the existing durable-record patterns with assignments and attributed
append-only evaluations, optional contributions, corrections, and applicable
review roles. Use existing identity, revision, validation, and transactional
patterns rather than a parallel entity model. Exact storage/API shapes are
settled with their callers during implementation.

An assignment, exposure to content, completed assessment, skip, and failure must
remain distinguishable. They need not each introduce a separate table. One
logical assignment can have retries but at most one active reception vote;
a linked correction preserves the earlier statement. Independently assigned
later assessments are legitimate, even from the same model, without claiming
independence. Votes bind to the exact wording read, not the mutable chain head.

Define coverage applicability by artifact kind and review role. A missing
adapter/evaluator is an explicit unsupported or blocked gap. `Not applicable`
requires an intentional named policy and reason. A reception vote does not meet
an evidence-check obligation. Cover model-produced claims beyond hypotheses and
proposals; bound targeted meta-review without recursively requiring review of
every review. Coverage keeps history when revisions or context make review due.

### E2 — Shared publication and compatibility

Wire staging, encrypted payloads, journal recovery, catalog kinds, readers and
remote decoding together. Staging belongs in the writer's transaction;
automatic publication follows SPEC §9.1. Preserve pending/failure visibility
and idempotence through restart. A second instance must read, assess, and
contribute to a first instance's artifact through the authorized services,
without rewriting the producer's immutable records.

Use additive migrations; never edit applied migration bytes. Keep votes,
arguments, tallies, ranks, and content-derived assessments out of plaintext
catalog columns. Version the record envelopes and compatibility checks. Retain
historical triage advice as advice: no conversion of cohort rank into reception,
exposure, or an outcome prediction. Legacy advice can be identified separately
in history but must not falsely satisfy a new role's coverage requirement.

A rollback must retain new records and every required key, and either read them
correctly or explicitly refuse unsupported capabilities. Restoring a v1 recipe
alone is not a complete storage/binary rollback plan.

### E3 — Worker evaluation and bounded assignments

Add evaluation work through the existing worker result/validation/persistence
path, reusing its replay ledger. Expose role-specific read contexts: reception
voting initially withholds existing tallies, ranks, and earlier evaluations;
comparison/refinement can reveal them after the initial assessment. Audit the
actual brokered reads as well as the initial prompt for score leakage, and record
what was shown. This is procedural blinding, not erased model memory.

Accept a bare vote without synthesizing prose; accept a contribution without a
vote. Reject same-run self-boosting of newly authored alternatives. Preserve
model/profile/run/recipe/context provenance. A comparison may prefer B over A
in a named context but must not silently mint global votes for either.
Cancellation/resume leaves durable completed work and no duplicate votes.

### E4 — Coverage and budgeted selection

Build a shared coverage inventory independent of popularity and of the operator
opening a page. Periodic checks discover new artifacts, unfinished assignments,
material changes, and overdue initial reviews. The check can finish while review
work remains overdue: **coverage inspection completed** and **all eligible output
reviewed** are separate facts.

Reserve authorized attention for oldest-due eligible initial reviews, across
covered kinds. Allocate remaining review attention through a versioned weighted
policy: favor lightly reviewed revisions, reduce repetitive voting at both
stable reception extremes, preserve a positive exploration share, and restore
attention on material changes. Persistent disagreement gets a bounded diagnostic
challenge/comparison rather than an obligation to vote until consensus.

Keep the existing invitation/focus/disclosure boundaries and protected discovery
share. Account for each lane in one authorized allowance; concurrent workers
must not each treat the entire allowance as theirs. Claims, reservations,
completion, lease expiry, cancellation and failed delivery require explicit
recovery semantics. Unsupported sources and repeated skips consume bounded
attention and stay visible as gaps rather than receiving negative votes.

Choose documented settings for cadence, overdue thresholds, review targets,
weights, cooldowns, reserved shares, and spend ceilings during implementation.
This document does not invent universal numeric defaults. Persist the policy
version, random seed, captured input identities/freshness, assignment/role,
spend and stopping reason. Define what a policy change invalidates and what
historical inputs are retained for replay. A seed alone is not sufficient.

### E5 — Reading projections and decision support

Build the operator's ordering separately from E4's selection. Reuse the
rebuildable local-index pattern where it fits. Published immutable records remain
the durable source; projections carry policy/input identity, coverage and
freshness, and can be rebuilt without the producer's local database.

Apply recorded priorities, current work and pain, dependencies, Reality facts,
and permitted work before treating reception as a recommendation. Explicit
restrictions win over popularity, with each allowance's actual semantics
preserved: learn-only is not the same as excluding a subject from all learning.
Missing and conflicting context stays explicit. The same votes may yield a
changed recommendation when recorded work or reality changes.

Order the represented eligible set before pagination. Page reads have bounded
payload work; they do not scan/decrypt every evaluation. Show stale or incomplete
coverage and a usable ordinary browse order when ranking is unavailable. Do not
label producer-local tallies as deployment totals. Choose a snapshot/cursor or
otherwise explicit pagination consistency contract for concurrent changes.

Compare alternative remedies for one problem while retaining individual votes,
records, and decisions. Explain why-now, the unresolved objection, and what
might change the recommendation where known. Unknown explanations are allowed;
do not require a model to fabricate a rationale for a bare vote. Prevent repeated
near-identical proposals from occupying the entire recommended view by volume.

Keep feedback attributed and scoped. Reasons such as not-now, wrong-problem, and
wrong-remedy may accompany an existing explicit disposition; collecting the
reason alone must not silently create one. Reuse operator context where possible
rather than introducing a second decision vocabulary. A descendant addressing
an old refusal is evaluated on its new merits. No preference is inferred from
clicks, dwell time, or ignored cards; no global focus rule is silently installed.

### E6 — Outcomes, criteria, and reconsideration

Add implementation and outcome assessments linked to the accepted revision,
criterion version, sources, environment, time, and uncertainty. Babel may record
these assessments without second human confirmation, but cannot execute the
proposal or ungranted checks. Missing/partial/conflicting evidence is not success;
a merge is not deployment and deployment is not proof of the promised outcome.

Criteria resolved after acceptance remain identifiable as later decisions.
Babel can suggest them but cannot rewrite its target and verify itself against
the replacement. Keep operator acceptance, observation and inference distinct.
Display contrary evidence beside earlier verification, with scope and date,
rather than choosing a last-writer-wins success badge.

Material changes on rejected/deferred/verified work create grouped Reconsider
items explaining what changed. Preserve the prior decision; only an explicit
operator action reopens it. Hypotheses receive claim/evidence/relevance review
without being forced into implementation lanes.

### E7 — Browser integration and active-policy cutover

Deliver the full SPEC §8.5 navigation: Recommended and alternative sorts,
Unreviewed/coverage, proposal lifecycle lanes, Reconsider, hypotheses and other
covered output, revision/evaluation histories, sources, grouped alternatives,
criterion resolution and operator decisions. Coverage is role-specific, including
blocked/unsupported/overdue work and when its last check finished. Policy/budget
configuration and current work status are visible; saving settings does not
start compute by itself. Remote run launch remains a separate capability.

Use the same application services for browser and headless callers. Keep TypeScript
DTOs, mocks and rebuilt embedded `web/dist` consistent when implementation lands.
Exercise real navigation, interactions and history on the standalone browser;
this reading/configuration surface does not wait for paused Manifold work.

Cut active recipe and duty callers over with the compatible services. Bump the
recipe version and semantic digest together; retain historical recipe/advice
readability, but no obsolete active writer path maintained as a parallel policy.
Activation requires all stages and SPEC §14 acceptance, not merely packing or
publishing a new binary. Live deployment remains operator-owned.

## 4. Consumer acceptance matrix

These are future executable scenarios, not tests claimed to have passed.
Use isolated synthetic HOME/XDG/storage and disposable catalog/object stores,
never the live deployment. Keep tests for plausible authority, integrity,
idempotence, budget and lifecycle failures; do not pin incidental wording.

| Scenario | Required observation |
| --- | --- |
| Bare support, opposition and uncertainty on A; read on B | Attribution and exact revision survive automatic publication; no prose invented; a contribution may exist without a vote |
| Retry one assignment; separately skip another; cancel before assessing a third | One active assessment for the first; no vote/completion from the latter two; exposure and attempt history remain distinguishable |
| Review revision n, then revise during an assignment | Votes stay on the revision read; no endorsement silently moves to n+1; a stale result never represents current-context coverage |
| Two workers claim one assignment; a stale worker returns after takeover | Only the valid claim commits; no duplicate active vote or overspent shared allowance; completed work survives resume |
| Blind initial review, then reveal for comparison | Actual served content/read tools withhold prior evaluations initially; reveal is attributed; same-run self-boost is refused |
| Fixed policy and captured inputs; reserved coverage plus random selection | Replayable draws, valid weight/exploration behavior, stable-reception cooldowns, material-change attention and bounded disagreement work; random selection does not promise every individual draw prefers the highest weight |
| Old, never-reviewed observation or finding on a non-producing instance | Found regardless of score or review-queue enrollment; due initial review progresses in its reserved allocation |
| Coverage check finishes with insufficient review budget or a missing evaluator | Check completion is visible alongside overdue/unsupported work; nothing falsely becomes reviewed or not applicable |
| Same votes; changed recorded current work or pain | Recommendation changes where relevant, with provenance and freshness; inferred interest cannot override explicit policy |
| Projection unavailable, then rebuilt; concurrent publication while paging | Honest fallback/coverage, globally ordered pages under the declared consistency contract, and bounded page-read work as corpus grows |
| Compare two remedies; operator gives a scoped wrong-remedy reason | Individual records and decisions remain; no automatic vote/merge/ruling; a revision addressing the reason can regain relevance |
| Open or ignore cards without giving feedback | No preference, endorsement or refusal is inferred; decision-changing unknowns remain visible rather than invented |
| Accepted without settled criteria; partial implementation; later contrary evidence | No unqualified verified state; later criteria are attributed; evidence scopes, disputes and prior decisions remain readable |
| Material change on rejected/deferred/verified work | Reconsider appears once per material change, with linked reports; prior decision remains; reopening is explicitly operator-driven |
| Browser-only navigation through full lifecycle on two independent instances | All covered artifacts, sorts, histories, decisions and coverage are reachable without guessed URLs; same inputs/policy yield consistent views |
| Historical v1 advice and new records across upgrade/rollback | Advice stays advice; no synthetic votes/exposures; newer records are retained and unsupported readers fail explicitly rather than discard them |

Measure coverage by kind/role and overdue age, review exposure and skips, spend by
allocation, unresolved disagreement, projection rebuild/page cost, and explicitly
reported usefulness. Report outcome verification and disputes separately from
operator acceptance. Contribution count and acceptance rate are not success
objectives: bare votes are valid and a useful pre-review may lead to refusal.
Do not introduce interaction tracking just to measure operator attention.

## 5. Implementation decisions to close before activation

These are engineering choices within the approved scope, not renewed questions
about whether to deliver the full lifecycle:

1. Record-family schemas, catalog kinds, subject validation across hosts, and
   additive migration/rollback compatibility. Prefer existing patterns; settle
   typed decoder ownership rather than overloading an unrelated kind casually.
2. Cross-worker assignment fencing and budget reservations, including interrupted
   claims, expiry, spend reconciliation and admission of new policy versions.
3. Coverage applicability/cadence and bounded meta-review, placement of periodic
   checks within existing accounted orchestration, and measurable policy settings.
4. Projection layout, rebuild/invalidation costs, encrypted-data handling, freshness
   bounds and pagination consistency under publication and changed Reality context.
5. Criterion resolution and scoped feedback storage, reusing operator context and
   dispositions without conflating observed outcomes with operator authority.

Recipe subject applicability and replay of unrelated existing discovery draws
may expose adjacent implementation gaps. They do not authorize silently expanding
this work into all cookbook focus handling or every conductor random path.
Record a dependency if evaluation actually needs one; keep unrelated work separate.

## 6. Product lesson, not an imported algorithm

[YouTube's public recommendation-system account (2021-09-15)](https://blog.youtube/inside-youtube/on-youtubes-recommendation-system/)
distinguishes clicks/watchtime from user-reported satisfaction, and satisfaction
from information quality. The useful lesson for Babel is objective separation:
reception, evidence, personal relevance, and observed outcomes answer different
questions. This is not a performance claim for Babel or a proposal to adopt
YouTube's behavioral tracking. Babel learns from explicitly attributed context
and feedback, not an engagement-maximizing feed.
