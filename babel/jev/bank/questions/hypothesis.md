---
kind: hypothesis
version: 2
---

# Hypothesis

A hypothesis is a claim a run put forward and nobody has ruled on yet, and it is the largest
family Babel holds. It is asked the whole panel, because what a hypothesis needs is exactly the
judgement a tally gives: many small independent opinions rather than one confident sort.

Every number in the thresholds block was measured on one deployment's imported Go-era corpus, at
one date, under one recipe and model set. It is calibration, not behaviour: re-fit it against what
this plugin's own intake produces before leaning on it, and read `docs/jev-case-study-audit.md` §0
before quoting any share here as if it described Babel in general.

## Thresholds

Each row is one side of one voter: the question it thresholds, the direction it casts, the line at
which the answer becomes an opinion, and the distribution over hypotheses that justified that
line. A row with no distribution is refused by `babel/jev/bank/parse.ts` rather than
seeded. A side firing on under 2% or over 95% of its kind answers the same way for everything and
is not admitted; it stays in the block, because deleting it would lose the measurement that
retired it.

| voter              | question           | casts | fires when    | observed                                 |
| ------------------ | ------------------ | ----- | ------------- | ---------------------------------------- |
| worth-of-attention | worth_first        | up    | `>= 2.7`      | `n=2453 fires=9.3% mean=2.032 sd=0.430`  |
| worth-of-attention | worth_first        | down  | `<= 1.3`      | `n=2453 fires=4.7% mean=2.032 sd=0.430`  |
| concreteness       | specific           | up    | `>= 0.7`      | `n=2453 fires=24% mean=0.447 sd=0.270`   |
| concreteness       | specific           | down  | `<= 0.3`      | `n=2453 fires=37.9% mean=0.447 sd=0.270` |
| contradicts-intent | contradicts_intent | up    | `>= 0.7`      | `n=2453 fires=18.3% mean=0.442 sd=0.227` |
| actionability      | actionable         | up    | `>= 0.7`      | `n=2453 fires=22% mean=0.454 sd=0.256`   |
| actionability      | actionable         | down  | `<= 0.3`      | `n=2453 fires=37.3% mean=0.454 sd=0.256` |
| evidence           | evidence_strength  | up    | `>= 1.5`      | `n=2453 fires=1% mean=0.444 sd=0.400`    |
| evidence           | evidence_strength  | down  | `<= 0.4`      | `n=2453 fires=55.9% mean=0.444 sd=0.400` |
| recurrence         | recurring          | up    | `>= 0.7`      | `n=2453 fires=46% mean=0.565 sd=0.326`   |
| friction-lens      | friction_kind      | up    | `is not none` | `n=2453 fires=48.5%`                     |
| freshness          | temporal           | up    | `is current`  | `n=2453 fires=69.3%`                     |
| freshness          | temporal           | down  | `is changed`  | `n=2453 fires=25.4%`                     |
| rigour             | speculative        | down  | `>= 0.75`     | `n=2453 fires=46% mean=0.708 sd=0.112`   |
| scope              | self_referential   | down  | `>= 0.7`      | `n=2453 fires=11.9% mean=0.388 sd=0.219` |
| editorial          | fused_to_fix       | down  | `>= 0.75`     | `n=2453 fires=12.6% mean=0.497 sd=0.189` |
| novelty            | restates_known     | down  | `>= 0.6`      | `n=2453 fires=7.4% mean=0.208 sd=0.186`  |
| trustworthiness    | needs_arithmetic   | down  | `>= 0.7`      | `n=2453 fires=10.1% mean=0.304 sd=0.239` |

Two rows are worth reading twice. **evidence**'s up side fires on 1.0% of hypotheses and
is therefore not admitted — a hypothesis is a claim _awaiting_ evidence, so asking whether its
material establishes it is asking a question the kind cannot answer; its down side, at 55.9%,
stands, because a hypothesis showing nothing at all is a real objection. And **rigour** objects to
46% of them, which is not a defect in the question: nearly half of these claims do assert a
mechanism their material does not demonstrate.

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

These distributions are fitted over the study's whole 974-record sample rather than per kind,
unlike the thresholds above: the questions in this block were asked of the corpus and not of one
document's slice of it, and a per-kind share nobody measured would be a number somebody typed.
Re-fit them per kind once this plugin's own intake has produced enough of each.

| question   | suggests        | fires when             | observed                                |
| ---------- | --------------- | ---------------------- | --------------------------------------- |
| overclaims | develop-further | `>= 3`                 | `n=974 fires=31.7% mean=2.227 sd=0.444` |
| overclaims | none            | `<= 1`                 | `n=974 fires=5.9% mean=2.227 sd=0.444`  |
| settleable | develop-further | `is query_own_data`    | `n=974 fires=37.5%`                     |
| settleable | develop-further | `is reading_code`      | `n=974 fires=33.2%`                     |
| settleable | ask-question    | `is needs_live_system` | `n=974 fires=17%`                       |
| settleable | develop-further | `is command_or_test`   | `n=974 fires=11.2%`                     |
| settleable | none            | `is not_settleable`    | `n=974 fires=1%`                        |
| settleable | draft-issue     | `is needs_new_work`    | `n=974 fires=0.1%`                      |
| vague      | develop-further | `<= 1`                 | `n=974 fires=54.9% mean=1.581 sd=0.876` |

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

### vague

type: score
asks: How concretely does `record` say what to do about what it describes?

- No action is implied at all.
- An area of concern, with no move named.
- An action, but in general terms.
- A specific action an agent could start on.

### settleable

type: choice
asks: What kind of check would settle whether `record`'s central claim is true?

- query_own_data — A query against the records, runs, edges or events Babel already holds; not for: Data Babel would first have to go and collect
- reading_code — Reading the code, configuration or documentation as it stands; not for: Anything that needs the system running
- needs_live_system — Watching a running system behave; not for: Anything a static read settles
- command_or_test — Running one command, test or build and reading what it prints; not for: A change that would have to be written first
- not_settleable — Nothing checks it; it is a matter of judgement; not for: A claim that is merely expensive to check
- needs_new_work — Nothing short of doing the work the record describes; not for: Work needed to act on the claim rather than to believe it

### overclaims

type: score
asks: How far does `record`'s stated confidence run ahead of the material it actually shows?

- The wording is more careful than the material requires: it claims less than what is shown establishes.
- The wording matches the material: every claim in it is carried by something shown.
- The wording runs ahead of the material: a claim or two rests on assertion rather than on what is shown.
- The wording runs well ahead of the material: the central claim is asserted rather than shown.

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

### hyp_b7f325486d75ca1599c876c2b7adce8f

provenance: standing
tally: +5

> In the -tmp SSH session the agent maintains outcome integrity under a hard verification ceiling:
> it cannot test the login because it does not hold the private key, and instead of claiming success
> it names the exact limit, retracts its own earlier fix as a no-op, substitutes the strongest
> available proxy check (ssh-keygen fingerprint parse), and warns the fix is not rebuild-durable.

Five up and nothing against: worth-of-attention, concreteness, actionability, friction-lens and
freshness all fired, and evidence did not, which is the right reading — the claim is about conduct
under a limit rather than about material that establishes it. It is the anchor for what a
well-formed hypothesis looks like: one session, one named mechanism, and the limit of what could be
checked stated rather than papered over.
