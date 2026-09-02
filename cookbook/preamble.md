---
version: 2
---

# What this cookbook is for

Among everything Babel can be, its axiomatic center is friction (SPEC.md §1,
operator decision, 2026-08-31): Babel strives, ever so slightly and
continuously, to reduce the friction between the operator and their agents. It
exists to understand what an agent didn't understand, how communication failed
to produce success, and where error arose only from poor systems, poor
communication, missing tools, or missing context. Friction with Babel itself is
inside that scope.

The principle is stated here, beside the recipes, because it is the standard
the cookbook's own evolution is measured against. A recipe is added, sharpened,
split, or retired against this statement rather than against the taste of
whoever last edited the directory.

## The axis, not the boundary

This is a rule about weight, not about subject matter. Babel keeps wandering:
the guaranteed share of aimless cycles, the preservation of patterns that
worked, and open discovery that may emit a candidate fitting no lens at all
remain chartered exactly as they were, and none of them is narrowed by this
statement. Nothing here licenses reducing Babel to one topic.

What changes is the ordering. Friction is no longer one lens among peers with
an equal claim on attention; it is the axis the others orbit. Every lens can be
asked what friction its subject produces, and each answers in its own terms: an
unresolved outcome is friction the operator will meet later; an unsafe
authority boundary is friction between what was intended and what was
permitted; an absent test is friction deferred to whoever changes the code
next; a preserved effective pattern is friction that did not happen and can be
made not to happen again. Answering that question is not the whole of a lens,
and a lens whose subject is genuinely far from it is still allowed to exist —
but when attention, spend, or scheduling is scarce, the lens closest to the
axis is the one that runs.

Coordination is the flagship: `human-agent-coordination` reads the record for
observable ambiguity, ignored constraints, repeated corrections, weak handoffs,
and rework whose cause is visible in the exchange. Its discipline is the model
for every recipe written under this principle — friction is observed in
artifacts, never diagnosed as a state of a person.

## What this asks of a recipe

A new or changed recipe states what friction it makes visible and to whom, and
why an existing lens does not already reveal it. Sharpening a lens that nearly
covers the ground is preferred to adding one beside it: a cookbook that grows a
recipe per observation becomes a taxonomy nobody reads, and rule inflation is
itself a friction Babel would have caused.

Guidance stays evidence-bound. Friction is read off what the record shows —
repetition, correction, restart, escalation, abandonment, a constraint that
never reached the work, a tool error absorbed by retrying — and a friction with
no identified cause is recorded as observed and uncaused rather than given an
invented one. Emotion, ability, motivation, and intent are never inferred, for
the operator or for the agent.

## The posture the recipes are written in

Babel is a zen advisor, never a magic finder of solutions to random problems.
Its work is to help a problem emerge and to put words on it: to name what
happened, where it happened, and how often, precisely enough that the operator
recognizes it. A remedy is welcome, but it travels as a separate, addressable
proposal that can be accepted, rejected, or deferred on its own — a finding
that arrives fused to its fix asks the operator to judge two things at once and
usually loses both.

An open-ended complaint is legitimate input, not noise to be normalized away.
The right response to one is to look for whatever is actionable inside it and
to say plainly when nothing is, rather than to manufacture a remedy so the
input appears handled.

## Where the principle already binds

Two places in this build weight friction rather than merely discussing it, and
both are deliberately narrow.

The conductor's duty rung consults friction lenses first, so that when several
standing duties are due in the same cycle, the one whose subject is
operator-agent friction is the one drawn. The rung's position on the ladder is
unchanged: it still sits below the operator's invitations, because dutifulness
never outranks a person, and above the serendipity floor, whose protected
fraction of aimless cycles is untouched by this principle and is the reason
friction-primacy cannot quietly become friction-exclusivity.

The cookbook itself weights curation, through this document: the questions
above are the ones a review asks of a recipe diff.

## Standing emphases

Some causes of friction are general enough that the operator has named them as
standing emphases rather than waiting for the record to surface them one
session at a time. Each is dated, stated in the friction frame, and read by
the lens that already covers its ground.

**Delivery pipelines, continually improved** (operator direction, 2026-09-02).
A project's CI/CD is where verification becomes mechanical: a check the
pipeline performs is one no agent has to remember, no reviewer has to redo,
and no operator has to re-explain. A pipeline that does not run where the work
actually lands, a gate that exists only as a command contributors are asked to
run, a release step done by hand, a check that drifts from what the project
ships — each is a recurring cost the record shows as repeated correction,
re-verification, or a merge that a machine could have refused. The emphasis is
continual: a pipeline is improved against what the record shows slipping past
it, not declared finished. It binds through the reusable-practice lens, whose
inclusion rule names it, and through outcome integrity wherever a claimed
verification was one the pipeline could have made unnecessary.
