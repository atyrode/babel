# The Jev case study, audited

An outside evaluation put TypeSafe's Jev — a judgement layer that scores and routes records — over
6,038 records, across sixteen chapters and eleven tested ideas. This document is what the
measurements mean for Babel: which numbers hold, which mechanism each one indicts, what shipped,
and what is now an issue.

## 0. What was measured, and what it is not

**Those 6,038 records are not Babel.** They are the output of **one deployment** — one operator's
instance, over his own agent conversations, at one date, under one set of recipes and one set of
models. Three things follow, and every number below has to be read through them.

**Every one of them was written by the retired Go implementation.** The plugin had never produced
a record until the settlement learned to write one: `settleSession` read an answer, checked its
citations and wrote a receipt, and the claims went nowhere. So the corpus measures what the
_previous_ implementation produced under the recipes it ran. The plugin's own intake path is new
and has never been measured at all.

**A share is a property of that output, not of the product.** "42.7% cannot name their codebase"
describes what those runs wrote about that operator's conversations. It is not a law of Babel, and
a different corpus, a different recipe set or a different model could move it. Where a proposal's
justification rests on such a share, the honest instruction is to **re-measure it against what the
plugin produces** before acting on the number — and every issue filed from one says so.

**A structural finding is different and carries no such caveat.** Nothing retrieves over the
corpus; `tell` wrote to a table nothing read; only the explore stage is ever dispatched. Those are
true of the code, whatever any deployment happens to hold.

**And every percentage is a range.** Option ordering moves an aggregate share by ten to twenty
points while per-record answers stay 72–79% stable (§5). Quote the range or quote nothing.

**None of that makes the corpus disposable, and the distinction is load-bearing.** Those records
are not a yardstick for the plugin's behaviour, and they _are_ the material the loop starts from:
the only real corpus Babel has, and the seed for retrieval, for memories, for calibrating what a
reviewer is worth, and for whatever judgement is applied retroactively (operator direction,
2026-09-19). So the caveat above is narrow. **Do not read a share off the corpus and call it
Babel's** — and equally **do not skip the corpus because its shares are unrepresentative.** A
feature that only ever applies to records not yet written cannot bootstrap anything, and a loop
that improves on itself needs something to improve from.

**Jev is optional by construction.** Nothing in Babel may ever depend on it. Where this document
names Jev work it names an enable-able part, `atyrode.babel.jev`, that reaches the baseline only
through its doors, and with the part absent every door, panel and conductor path behaves exactly
as it does now.

## 1. The numbers

Numbers are from `case_studies/jev-system-one/data/*.json` — the JSON, not the prose — with the key
path given. Three exist only as prose or as a reported query result and are marked. Every share in
this table is one deployment's Go-era output, per §0, and is a range per §5.

| finding                                             | number                                 | source                                                                                                                   |
| --------------------------------------------------- | -------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| records too vague to start                          | **22.2%** (24.0% on a disjoint sample) | `ideas.json:autopsy.why["too_vague"]` = 216 of n=974; `counter_tests.fresh_sample.too_vague`                             |
| records naming a concern with no action             | **50.1%**                              | prose only — `14-eleven-ideas-tested.md:129-133`; no JSON key holds the bucket                                           |
| records that cannot say which codebase they concern | **42.7%**                              | `ideas.json:addressing.repo["unclear"]` = 416 of 974                                                                     |
| records concerning Babel itself                     | **1.6%**                               | `ideas.json:addressing.repo["babel"]` = 16 of 974                                                                        |
| findings resting on a single run                    | **175 of 207 (84.5%)**                 | `ideas.json:lineage.distinct_runs`                                                                                       |
| proposals sharing their finding's run               | **116 of 116 (100%)**                  | `ideas.json:lineage.distinct_runs.proposal_same_run_as_finding`                                                          |
| claims settleable by querying Babel's own data      | **37.5%**                              | `ideas.json:settleable.repaired["query_own_data"]` = 365 of 974                                                          |
| retrieval: what the current mechanism finds         | **0.606**                              | `ideas.json:counter_tests.counterfactual_controls.retrieved_result_mean`                                                 |
| retrieval: the ceiling, a correctly matched record  | **2.043**                              | `…counterfactual_controls.derived.mean`                                                                                  |
| retrieval: a random record                          | **0.680**                              | `…counterfactual_controls.random.mean`                                                                                   |
| rejections in the status history                    | **1**                                  | `results.json:corpus.hypothesis_status.rejected`; the 4,850-event denominator is prose only (`12-the-flattening.md:235`) |
| records ever revised                                | **0 of 6,038**                         | prose only, from a reported `HAVING COUNT(*) > 1` returning no rows (`14-eleven-ideas-tested.md:373`)                    |

**The retrieval line is the most important number in the study, and it is not about judgement at
all.** Babel's content is good — a correctly matched record scores 2.043 out of 3 on real work. The
mechanism that picks a record scores 0.606, which is _below_ picking at random (0.680). Nothing a
scoring layer does improves that: it is a retrieval problem, and Babel has no retrieval.

## 2. Finding to mechanism

Each row names the state of the mechanism the finding indicts, with a path.

| finding                                            | mechanism                                                  | state                                                                                                                                                                                                                                                  |
| -------------------------------------------------- | ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 42.7% cannot name their codebase                   | a record carrying its repository                           | **not built.** `records.payload` has no repository binding, and `filings`/`entities` bind a topic rather than a commit. Tracked by #183, re-scoped onto the plugin.                                                                                    |
| 22–24% too vague, ~50% no action named             | a specificity gate at intake                               | **not built** (#334), and now reachable: until this week the plugin created no records at all, so there was no intake to gate.                                                                                                                         |
| 84.5% of findings rest on one run                  | showing how many runs a record rests on                    | **built.** `corroboration` on the `record` door, computed from `records.run_id` and the typed edges; the peel reads "three supports, from one run" at the evidence depth. It reports whatever a record rests on, so it needs no re-measurement.        |
| 84.5%, enforced rather than displayed              | marking a finding whose supports share one run at creation | **not built** (#329). It may not refuse the record, because 84.5% of the imported corpus would fail it.                                                                                                                                                |
| nothing proposes an unexamined lens-and-topic pair | the coverage grid                                          | **built.** `coverage` on the `topic` door, one row per recipe the policy declares, zeros included — and a zero the launch door can run offers a launch of that recipe scoped to that topic, through the door Watch's own form posts.                   |
| 37.5% settleable by Babel's own data               | classifying what would settle a claim                      | **not built** (#335). The study's most robust number.                                                                                                                                                                                                  |
| retrieval at 0.606 against a 2.043 ceiling         | an index over the corpus                                   | **not built.** `docs/parity.md`, the `index/` row: a preparation selects by recency or by topic, and nothing retrieves. This is the binding constraint (#337).                                                                                         |
| 1 rejection in 4,850 events; 0 revisions           | the operator's disposition path                            | **built and working.** `feed/rows.tsx` → `ask()` → `ruleAction` → `rule()` → an append-only `dispositions` row, read back as `MAX(seq)` standing. Nothing needed building; that deployment's records are unruled because nobody ruled on them.         |
| the operator's steering went unread                | reading `steering` back, and quoting it to a run           | **built.** The `policy` door answers his recent remarks and Watch renders them; an exploration's prompt quotes a bounded selection of the same projection as evidence, and the receipt names which remarks it carried and how many the bound left out. |

## 3. Three corrections a reader must not miss

1. **#301's remedy shipped** in `feb45cb`. `server/engine/review.ts` strips the withheld keys before
   a projection reaches a reviewer; the comment above it describes the old self-refusal in the past
   tense.
2. **The drawn-review lane dispatches.** Three documents said it answered `draw_pending`, and the
   brief for this audit repeated it. `dispatchReviews` (`server/conductor.ts`) draws, claims under a
   fence, blinds the projection, posts the review as a Code session and binds the claim to Code's
   job id, every cycle the policy enables. `draw_pending` appears nowhere in the plugin. What is
   genuinely owed is **evidence, not code**: every rehearsal of that lane has been synthetic.
3. **Babel-as-a-plugin had never produced a record.** Every one of the 6,038 the study measured is
   imported Go-era history: `settleSession` read an answer, checked its citations and wrote a
   receipt, and the hypotheses, observations, findings and proposals went nowhere. That gap was
   unfiled. It is closed, which is what makes the intake findings actionable at all.

One more, found while building the coverage grid: **`PolicyResult.recipes` is built from `runs`**, so
a recipe the hub holds and has never performed is invisible in Watch. The coverage grid therefore
reads the policy's declared list instead (#344).

## 4. Question-design rules

Reference for anyone writing a typed question later. These are the study's own methodology findings
and are deliberately **not** filed as issues: they are constraints on how to ask, not work to do.

- **A question that answers the same way for everything is broken, not calibrated.** Three of the
  study's own did: `checkable` at 92.8% (`ideas.json:settleable.degenerate_noul_pct`), `overclaims`
  at 92.4% (prose), `overreach` at 96.7% (`ideas.json:lineage."finding<-observation".overreach_pct`).
  A Score over described levels survived where the yes/no form did not.
- **Never ask about a set.** Two records judged both ways agreed only 33.8% of the time — the winner
  flipped in roughly two cases of three — and slot `b` won 82.1% regardless of content
  (`ideas.json:canonical_pairwise`). Ask about one record.
- **Validate against a known answer before trusting a result.** Four of eleven ideas produced a
  confident number that a control killed or downgraded. The vagueness question survived because it
  was reverse-coded and correlated at **r = −0.779**
  (`ideas.json:counter_tests.vagueness_reverse_coded`).
- **Do not ask for data the state cannot hold.** A question needing more context than the window
  allows produces a confident answer about the part it saw.
- **Tone moves judgement, so a judgement about tone must be independent of it.** Hedging a record
  whose facts were verifiably unchanged cost it **0.48** on worth and **0.68** on evidence strength
  — 30× and 43× the model's own repeat-noise floor — and re-routed **12 of 37** findings
  (`rhetoric.json:matched_37`).

## 5. Result stability

- **Option order moves aggregate shares 10–20 points.** Reordering the same question three ways over
  the same 250 records moved "too vague" from 30% to 50% and "repository unclear" from 39% to 53%.
  One axis was flat: `query_own_data` stayed at 38%, which is why §1's 37.5% is the number to lean
  on (`ideas.json:counter_tests.option_order`).
- **Per-record answers are 72–79% stable** across those orderings. The instability is in the
  aggregate, not the judgement.
- **A disjoint 400-record sample replicates every headline share within 4.6 points**
  (`ideas.json:counter_tests.fresh_sample`).
- Conclusion, in the study's words: sampling contributes a few points, question wording contributes
  ten to twenty.

## 6. The study's verdict, and this repository's answer

The study's own verdict is _not yet_, and it is specific: "Babel's binding constraint is not
ranking, routing, deduplication or attention. It is that most records do not say what they are about
or what to do... No sorter fixes that. It is fixed where records are written, and the one place Jev
clearly earns its keep on this evidence is at intake."

That is answered in three parts:

1. **The two improvements that needed no model shipped** — distinct-run corroboration and the lens
   coverage grid — plus the operator's steering, read back on the policy door and quoted to a run's
   prompt, because a table nothing reads is a worse defect than either. The remaining absences are
   #328–#345.
2. **The intake gate is #334, Jev's one clear win**, and it is only now reachable, because the
   intake path itself did not exist until this week.
3. **Retrieval is #337, the binding constraint**, and it is not Jev work at all.

## 7. Issue triage

What was done with the 74 open issues, and why.

| issues                                      | disposition   | reason                                                                                                                                                                                                                                                                                          |
| ------------------------------------------- | ------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| #140–#143, #145–#155, #160–#162, #181, #189 | closed        | Each specifies a step of becoming a Manifold plugin — the manifest, the action-door API, the agent-kind principal, the plugin surfaces, the run projection, retiring the Go web tree. Babel **is** that plugin; each is either shipped under a different shape or superseded by the fact of it. |
| #305                                        | closed        | Its premise — that the output contract was the bottleneck — was retracted by #311's corrected analysis, which found a Manifold output-size bug.                                                                                                                                                 |
| #279, #251                                  | closed        | Implemented and released.                                                                                                                                                                                                                                                                       |
| #301                                        | closed        | Remedy landed in `feb45cb`; its surviving observability criterion is refiled.                                                                                                                                                                                                                   |
| #247                                        | closed        | This sweep **is** P7, and its parity review is `docs/parity.md`.                                                                                                                                                                                                                                |
| #144                                        | **kept open** | Its host-identity substance resurfaced as the live bug #312. Cross-referenced rather than closed.                                                                                                                                                                                               |
| #75, #112, #134, #139, #223, #224, #225     | parked        | Still true, not next. Each names a design or a drill with no current claim on attention.                                                                                                                                                                                                        |
| #183                                        | re-scoped     | It already asks for records to carry their repository and was blocked on a Go-era design. It is the 42.7% finding, and it lands in `records.payload` with `entities`/`filings` as the topic binding.                                                                                            |
| #156, #223                                  | commented     | The retrieval measurement (0.606 against a 2.043 ceiling and a 0.680 random baseline) is evidence for both rather than a new issue.                                                                                                                                                             |
| #268                                        | commented     | The drain epic is throughput, not the operator's ruling motion, so it does not gate first use.                                                                                                                                                                                                  |
| everything else                             | untouched     |                                                                                                                                                                                                                                                                                                 |
