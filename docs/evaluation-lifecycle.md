# Evaluation: how a record is reviewed

[`SPEC.md` §4.12](../SPEC.md#412-evaluation-reception-and-observed-outcomes) states what
evaluation is for, [§5.8](../SPEC.md#58-backlog-first-evaluation-within-a-budget) states that it
is backlog-first within a budget, and
[§8.5](../SPEC.md#85-reading-order-and-lifecycle-views) states how the results are read. This
document owns the mechanism: the tables, the policy, the cycle, and what a reviewer is and is not
shown.

The rule the whole design rests on: **Babel votes and the operator rules.** A review is an
attributed judgement about a record, recorded append-only. It changes no record, settles no
question, and authorizes nothing. Only the operator's disposition does that.

## 1. The tables

Every table is in `babel/store/schema.ts` and every name below is that file's.

| Table            | What it holds                                                                                                                                                                                                    |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `records`        | the claims themselves — hypothesis, observation, finding, proposal — immutable by trigger, so a correction is a supersession and never an update                                                                 |
| `status_events`  | a record's lifecycle, append-only; the newest row is its status                                                                                                                                                  |
| `assessments`    | what a reviewer judged: the record, **the exact revision read**, the role, the vote, the reasoning or the filing/backlog result, as JSON. A correction supersedes the earlier statement rather than replacing it |
| `claims`         | who is entitled to review what, under a fence and a lease renewed by the job that holds it; finished rows are the spend ledger                                                                                   |
| `review_actions` | immutable typed submissions, action keys, correction links, committed effects and durable receipts; an invalid action writes none of them                                                                        |
| `policies`       | the evaluation policy, versioned; the newest row is in force, and only the operator writes one                                                                                                                   |
| `feedback`       | the operator's scoped reason on a record — and `question = 1` marks what the next review must answer                                                                                                             |
| `dispositions`   | the operator's rulings, append-only, the newest per record its standing                                                                                                                                          |
| `budgets`        | a bounded overlay on the policy's spending, with its own TTL                                                                                                                                                     |

Three properties follow from the shapes rather than from discipline:

- **A vote binds to the wording it was formed against.** `assessments.revision_id` is recorded
  with the vote, so revising a record after a review never moves that reviewer's endorsement onto
  text they did not read.
- **Nothing is retracted, only superseded.** Every one of these tables is append-only, several by
  `RAISE(ABORT)` trigger, so a reviewer's earlier statement remains readable beside the correction.
- **A reception vote is not an evidence check.** Votes carry their role, and a role's obligation
  is not met by another role's answer.

## 2. Roles

The roles a review may take are spelled once, in `babel/contract.ts`:
`reception`, `evidence`, `challenge`, `comparison`, `outcome`, `relevance`, and the two lanes of
topic maintenance, `filing` and `backlog`. A vote is `support`, `oppose` or `unsure`.

**A bare vote is a complete contribution.** After reading an exact revision, a reviewer may
record support, opposition or uncertainty with no comment, no new evidence and no original
argument. In typed mode, review completion still requires an accepted marker and successful
native settlement. Support means _this deserves the operator's consideration_; opposition
means _put it lower in the reading order_. Neither means the claim is proven or that anything
is authorized. A skip is not a vote, and a failure is not a vote: an assignment that produced
neither stays visible as a gap.

## 3. The policy, and the gate

`PolicySchema` in `babel/store/coordinator.ts` is the whole of what evaluation is
authorized to do. `enabled` defaults to **false**: turning evaluation on is one recorded operator
decision written through the `setPolicy` door, never a migration and never a config file.

The policy carries, in one document: the cadence; `batchSize`, `concurrentPerMachine` and
`leaseSeconds`, which bound how many assignments may be held at once, per machine, and for how
long; `perCycleCost` and `dailyCost`, the spending ceilings; the five reserved shares —
`coverageShare`, `explorationShare`, `discoveryShare`, `filingShare`, `backlogShare` — which are
the protected allocations across lanes; and the review route, `review.machineId`,
`review.profile`, optional `review.agentId`, `review.roleRecipes` and `review.recipes`.

A policy that cannot be honoured is refused with the sentence saying why, because each such
setting would make some other part of the system lie: an unversioned policy could never be
replayed against; a zero exploration or discovery share removes a protected allocation; shares
totalling over one over-commit a cycle, so one lane's reservation would silently come out of
another's; a cap below the initial reviews leaves a role permanently under-reviewed while the
record reads finished; a daily ceiling below one cycle's makes the per-cycle bound decorative.

**The recipe bodies travel with the policy version.** `review.recipes` carries each method's text,
not just its name, so a later edit to the cookbook cannot change the method an in-flight
assignment is being carried out under. The bodies are seeded from
`babel/store/recipes.seed.json`.

**A drain is not a policy edit.** `setBudget` records a bounded overlay with a TTL and a required
reason; the standing policy is untouched. That separation is load-bearing: an assignment id is
derived from the policy version, so editing the standing policy mid-flight mints new ids for
subjects already claimed and leaves the old claims holding their slots.

## 4. One cycle

`tick()` in `babel/server/conductor.ts` is one cycle, and it is four things in a
fixed order: the policy decides whether the loop exists at all; the beat's schedule is reconciled;
finished jobs are ingested; and each receipt settles its claim. Review dispatch happens inside it,
in `dispatchReviews`:

1. **Draw.** The coordinator picks a candidate for a role under the reserved lanes, the cooldowns
   and the per-machine bound. A draw that picks nothing returns a `Stop` with its reason, and every
   candidate declined on the way is a `Gap` carrying the record, the role and why — `claimed`,
   `cooling`, `capped`. "Nothing to review" and "starved by siblings" and "the coordinator refused"
   are therefore three different answers rather than one exit code.
2. **Claim.** The assignment is claimed under a fence, so two workers cannot both hold it and a
   stale worker returning after a takeover cannot commit.
3. **Project, blinded.** §5.
4. **Dispatch.** The review prompt is composed from the recipe the role names and the blinded
   projection, then measured against Code's byte bound. A `runs` row retains the intent before
   posting through `atyrode.code.runSession`; the claim is bound to the acknowledged job id.
   An already-authorized `review.agentId` selects a bounded generic Run with the
   `reviewAction` tool. Only an exact existing optional publication approval and explicit
   `services:invoke` grant with root-subtree reach may add Jev's namespaced judgment tool on
   that same Run. Missing Jev permission keeps ordinary typed review at its original root-node
   scope; absent or definitively unavailable ordinary tool authority selects identified,
   validated text instead. The mode is pinned before model execution. Optional Jev grades only
   the state the agent assembles, with host/store-bound scope and at most three attempts; it
   never submits an assessment. Ready metadata is not proof of credit, and invocation refusal
   means no judgment, not a review failure or fabricated vote.
5. **Commit actions.** In tools mode, each assessment or refinement passes shape, evidence
   and assignment-scope validation before one transaction writes its rows, receipt and partial
   submission state. Invalid actions write nothing; retries return the same receipt; corrections
   append linked successors. A completion marker requires an active assessment (including an
   explicit skip) and names every active accepted action exactly once; refinements alone cannot
   finish an assignment. The marker does not itself complete the review. Prose cannot submit
   a tools-mode answer.
6. **Settle.** A later cycle reads the native session through `code.readSession`. A successful
   exit with a marker, still-bound claim and no Stop completes a typed review; other endings
   keep acknowledged work partial. Text-mode answers still pass the authoritative validator
   before settlement writes any accepted rows. The terminal receipt and typed call trace commit
   together, including when Stop closes a terminal cancellation, and claim accounting is
   recoverable. Refused submissions still count as spend; unknown cost is charged at the
   reservation rather than treated as free.

The loop has no clock. A plugin may not poll as an alternate scheduler, so a tick runs when the
plugin's own dispatch wakes it or when one of its jobs settles. Claim fences prevent overlapping
wakes from buying the same review. Accepted action keys are idempotent; unknown native
tool postings are held, not retried, because the pinned Code/OMP path cannot combine tools with
a posting key. No text fallback can replay committed tool work. Stop fences the retained
intent immediately, including when the posting has not acknowledged its job. If OMP's named
pre-execution refusal subsequently proves that stopped intent posted no session and no action
was accepted, it closes at zero and releases the reservation without starting text fallback.
An uncertain posting remains retained.

**A paid-but-refused review is not a free failure.** The park heuristic reads the spend ledger: a
streak of reviews that reached no model and produced nothing parks the loop with a stated reason
until one is answered, an hour passes or a new policy is installed. A streak that cost money is
not that, and does not park it.

## 5. Blinding, and what it is not

An initial review reads the record and not its reception. `blinded()` in
`babel/server/engine/review.ts` strips the withheld keys — score, priority,
existing assessments, the per-role tallies — from the projection before it is composed into the
prompt, and `blindedLeak()` asserts afterwards that none survived. The stripping is deliberate
rather than a check that refuses: an imported record carries the very keys a reviewer is not shown,
and a leak check used as the filter meant no imported record could ever be reviewed at all.

This is **procedural blinding, not erased memory**. It withholds what the served projection
carries; it cannot unsee what a model already knows. The honest claim is that Babel did not show
the reviewer the tally, and that claim is checkable against the dispatch.

## 6. What the operator does

Four doors, and none of them is a vote (`babel/contract.ts`):

- `rule` — accept, reject, defer, duplicate, reopen or refine. Append-only; the newest ruling is
  the standing one; nothing else in the system writes a disposition.
- `comment` — attributed prose on a record, in its thread.
- `answer` — the operator's answer to a Reality question a run raised.
- `tell` — free-text steering, kept as its own record.

A ruling is the boundary. Babel's reviewers rank, argue and record outcomes; only the operator's
acceptance creates an entity, asserts a fact or applies a plan. Feedback the operator gives —
not-now, wrong-problem, wrong-remedy — is recorded as a reason beside an explicit ruling and never
silently becomes one, and nothing is inferred from what he opened, ignored or scrolled past.

## 7. Where the mechanism is short of the specification

Named here because a reader deciding whether to enable evaluation needs both halves.
`docs/parity.md` is the full list.

- **No live review has ever run.** The lane is whole in code and unproven in the world: the
  evidence is a conductor regression over the real SQLite store with a simulated Code receipt
  (2026-09-16), plus the gate. No model has answered a drawn review, no assessment on a real hub
  carries a metered cost. `docs/runbook.md` §11.7 lists what is owed, in order.
- **A drawn review cannot be launched directly, by design.** The `launch` door answers
  `draw_managed` for `review-backlog` and `file-and-tidy`: an operator-picked record would bypass
  the shared claim, the reserved lanes and the budget that the coordinator exists to arbitrate.
  The way to hurry a draw is a wake, not a bypass.
- **Only the `explore` stage ever runs.** `challenge` and `synthesize` exist as schemas in
  `babel/machine/results.ts` and as prompts in
  `babel/server/engine/prompts.ts`, and nothing dispatches them, so nothing
  criticizes a claim across runs.
- **A cycle's stop is not readable by the operator.** The cycle computes its `Stop` and its `Gap`s
  with their reasons, and no door exposes them, so "why did nothing happen this hour" is a
  question only the code can answer today.
- **Corroboration is displayed, not enforced.** Whether a finding's supports come from more than
  one run is a property of the rows; nothing refuses a record for resting on one.

## 8. The lesson this design is built on, and the one it refuses

[YouTube's public account of its recommendation system](https://blog.youtube/inside-youtube/on-youtubes-recommendation-system/)
separates clicks and watch time from user-reported satisfaction, and satisfaction from information
quality. The transferable part is objective separation: reception, evidence, personal relevance and
observed outcomes answer four different questions, and collapsing them into one score is what makes
a ranking unaccountable. That separation is why the roles in §2 exist.

What is refused is the rest of it. Babel learns from explicitly attributed context and feedback,
never from engagement: no click, no dwell time and no ignored card is evidence of anything, and
nothing here is a performance claim.
