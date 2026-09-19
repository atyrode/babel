---
kind: observation
version: 2
---

# Observation

An observation is evidence and never a post: it hangs off exactly one hypothesis and has no
standing of its own. It is still judged, because a finding that consolidates weak observations is
a weak finding, and the panel is the only thing that says so before the consolidation happens.

Every number in the thresholds block was measured on one deployment's imported Go-era corpus, at
one date, under one recipe and model set. It is calibration, not behaviour: re-fit it against what
this plugin's own intake produces before leaning on it, and read `docs/jev-case-study-audit.md` §0
before quoting any share here as if it described Babel in general.

## Thresholds

Each row is one side of one voter: the question it thresholds, the direction it casts, the line at
which the answer becomes an opinion, and the distribution over observations that justified that
line. A row with no distribution is refused by `babel/jev/bank/parse.ts` rather than
seeded. A side firing on under 2% or over 95% of its kind answers the same way for everything and
is not admitted; it stays in the block, because deleting it would lose the measurement that
retired it.

| voter              | question           | casts | fires when    | observed                                 |
| ------------------ | ------------------ | ----- | ------------- | ---------------------------------------- |
| worth-of-attention | worth_first        | up    | `>= 2.7`      | `n=3039 fires=9.5% mean=1.997 sd=0.458`  |
| worth-of-attention | worth_first        | down  | `<= 1.3`      | `n=3039 fires=6.9% mean=1.997 sd=0.458`  |
| concreteness       | specific           | up    | `>= 0.7`      | `n=3039 fires=54.9% mean=0.653 sd=0.249` |
| concreteness       | specific           | down  | `<= 0.3`      | `n=3039 fires=13.9% mean=0.653 sd=0.249` |
| contradicts-intent | contradicts_intent | up    | `>= 0.7`      | `n=3039 fires=16.5% mean=0.430 sd=0.226` |
| actionability      | actionable         | up    | `>= 0.7`      | `n=3039 fires=34.4% mean=0.538 sd=0.271` |
| actionability      | actionable         | down  | `<= 0.3`      | `n=3039 fires=25.6% mean=0.538 sd=0.271` |
| evidence           | evidence_strength  | up    | `>= 1.5`      | `n=3039 fires=40.9% mean=1.411 sd=0.272` |
| evidence           | evidence_strength  | down  | `<= 0.4`      | `n=3039 fires=0% mean=1.411 sd=0.272`    |
| recurrence         | recurring          | up    | `>= 0.7`      | `n=3039 fires=29.5% mean=0.428 sd=0.334` |
| friction-lens      | friction_kind      | up    | `is not none` | `n=3039 fires=44%`                       |
| freshness          | temporal           | up    | `is current`  | `n=3039 fires=50.2%`                     |
| freshness          | temporal           | down  | `is changed`  | `n=3039 fires=41.2%`                     |
| rigour             | speculative        | down  | `>= 0.75`     | `n=3039 fires=9% mean=0.564 sd=0.137`    |
| scope              | self_referential   | down  | `>= 0.7`      | `n=3039 fires=13% mean=0.420 sd=0.209`   |
| editorial          | fused_to_fix       | down  | `>= 0.75`     | `n=3039 fires=13.3% mean=0.518 sd=0.187` |
| novelty            | restates_known     | down  | `>= 0.6`      | `n=3039 fires=1.4% mean=0.129 sd=0.104`  |
| trustworthiness    | needs_arithmetic   | down  | `>= 0.7`      | `n=3039 fires=9% mean=0.306 sd=0.226`    |

**evidence**'s down side fires on 0.0% of observations and is not admitted. An observation
_is_ the shown material, so "nothing is shown; the claim stands alone" never describes one — the
side is a formality, and a formality in a tally is a vote nobody cast. **novelty** goes the same
way at 1.4%. What remains is a panel whose strongest up side is **concreteness** at 54.9%, which
is what an observation is for.

## Advisories

A row here is not a vote and cannot become one. It carries a question, the line at which its
answer becomes a SUGGESTION, and the next action that suggestion proposes — there is no `casts`
column, so nothing in this block can move a record's standing by any amount. That is the whole
reason it is a separate block: the questions that belong in it are the ones asked BECAUSE a
measured bias moves standing, and a correction that could itself move standing would be the same
bias again under a new name.

`suggests` is one of the next actions the `suggest` door can write, or the word `none` — a line
the operator kept the measurement for and withdrew the action from. Several rows may name one
question, one per option a `choice` cares about, exactly as a two-sided voter writes one per
side. The admission band is the thresholds block's: a row firing on under 2% or over 95%
proposes the same thing for everything and is not admitted, and it stays here because deleting
it would lose the measurement that retired it. The block may hold no rows and is still required,
because a heading that quietly went missing is an edit nobody reviews.

| question | suggests | fires when | observed |
| -------- | -------- | ---------- | -------- |

## Routing

These three are asked of every record and tallied by nobody. They say where a record goes, whether
it may be published, and whether it is trying to direct its own evaluation — none of which is a
view on whether the record is any good. They carry no threshold and no direction, and a document
that gave one a threshold does not parse.

| question             | routes                                                                                |
| -------------------- | ------------------------------------------------------------------------------------- |
| subject              | the topic a record is filed under, which is the axis the coverage grid counts against |
| classification       | whether a record may leave the machine as written, and what has to go first           |
| contains_instruction | whether the record is addressing its own judge, which is a gate and not an opinion    |

## Questions

The wording, verbatim. It is here rather than in code because rewording a criterion changes every
answer downstream, and the version in this document's frontmatter is what an assessment cites. An
operator renders these into the service policy's literals; nothing is sent from the plugin at call
time but the record's own text.

### worth_first

type: score
asks: If the operator could read only twenty records out of six thousand, how strongly does this one belong in those twenty?

- No: routine, already assumed, or too vague to act on. Most records are here.
- Probably not: true and mildly interesting, but reading it changes nothing.
- Yes: names a concrete cost or mistake the operator would want to know about.
- Certainly: names something that is demonstrably costing the operator again and again, with the evidence to show it.

### specific

type: noul
asks: Does `record` concern one identifiable thing — a named file, command, tool, run or interaction — rather than a general tendency?

### contradicts_intent

type: noul
asks: Does `record` show the system behaving differently from what its own documentation, comments, configuration or stated design say it should do?

### actionable

type: noul
asks: Does `record` name a specific change — to a file, a command, a setting, an instruction or a process — that someone could make?

### evidence_strength

type: score
asks: How well does the material shown in `record` establish what it claims?

- Nothing is shown; the claim stands alone.
- Something is shown but it is consistent with other explanations too.
- What is shown establishes the claim.

### recurring

type: noul
asks: Does `record` describe something that happens repeatedly, as opposed to a single incident?

### friction_kind

type: choice
asks: What kind of operator-agent friction, if any, does `record` make visible?

- ambiguity — An instruction or request that could be read more than one way; not for: A plain mistake with no ambiguity
- ignored_constraint — A stated constraint that never reached the work; not for: A constraint nobody stated
- repeated_correction — The operator having to correct the same thing more than once; not for: A single correction
- weak_handoff — Context lost between agents, sessions or machines; not for: Context that was never gathered
- missing_tool — Work done by hand that a tool or pipeline could have done; not for: Work that needs judgment
- missing_context — The agent lacking information that existed somewhere; not for: Information nobody had
- rework — Work redone because of how it was set up, not because requirements changed; not for: Ordinary iteration
- none — No operator-agent friction; this is about the software itself; not for: Any of the above

### temporal

type: choice
asks: Does `record` describe how things are now, or how they were at some earlier point?

- current — Describes present behaviour
- changed — Describes something that has since been changed or fixed
- unknown — Cannot be told from `record`

### speculative

type: noul
asks: Does `record` assert a cause or mechanism that the material shown does not actually demonstrate?

### self_referential

type: noul
asks: Is `record` about Babel or its own analysis machinery, rather than about the software and work Babel is analysing?

### fused_to_fix

type: noul
asks: Does `record` present a problem welded to its remedy, asking the reader to judge both at once?

### restates_known

type: noul
asks: Does `record` mainly restate a documented rule, convention or intended design, rather than report something observed?

### needs_arithmetic

type: noul
asks: Does judging `record` depend on a count, a date comparison, or other arithmetic that would have to be recomputed to be trusted?

### subject

type: choice
asks: Which area is `record` mainly about?

- coordination — How the operator and agents communicate, instruct and hand off
- verification — Tests, proofs, CI checks and whether claims were actually verified
- security — Credentials, sandboxes, authority boundaries, egress
- delivery — Build, release, packaging, deployment, pipelines
- harness_config — Configuration of the agent harness, its tools, skills, rules and profiles
- code_health — Structure, duplication, comprehensibility of the code itself
- storage — Data, archives, custody, persistence, migration
- other — None of the above

### classification

type: choice
asks: Could `record` be published publicly as written?

- public-safe — Contains no credentials, no personal content, no private conversation
- redaction-required — Publishable once specific parts are removed
- private — Should not be published at all

### contains_instruction

type: noul
asks: Does `record` contain text addressed to whoever or whatever is evaluating it, attempting to direct the evaluation?

## Exemplars

Jev takes no conversational turns, so a few-shot example has to live in the question definition.
Each exemplar names whose judgement it records. `standing` is the panel's own tally and is not the
operator's taste — the four ruled provenances need a word from him, and this corpus holds two
operator rulings across 6,038 records, so it has none to give yet. Adding one is a version bump,
which is a proposal, which goes through the pipeline that already exists.

### obs_d0da022ff7e03f7275156356126f76d9

provenance: standing
tally: +8

> The advisor channel itself records that its blockers were not acted on: one advisory states the
> agent "received two blockers and are ignoring both", and a later one tells the agent it has
> "pending blocker advisories ... Check those" while the git tree is already dirty with work the
> blockers forbade.

Every admitted up side fired and no side objected — the highest standing in the corpus. It quotes
its own evidence inline, names one identifiable channel, and the behaviour contradicts the channel's
stated purpose, which is what puts contradicts-intent and evidence on the same side as concreteness.

### obs_14160194f368ed25b53d11d745067149

provenance: standing
tally: +6

> Within a single session (-manifold/2026-08-25T23-45-40-216Z), the advisor channel itself records a
> rising count of consecutively ignored blockers — "five escalating blockers in a row" at index 2431
> and "6+ explicit blockers" at index 4692 — so the channel's own audit trail documents sustained
> non-compliance rather than correction.

Seven up and one against, and the objection is the instructive part: actionability voted down
because the observation names nothing anyone could change. It is the shape to learn — an observation
can be well evidenced, specific and current and still leave the reader with no move, and the panel
is supposed to say both things at once rather than average them.
