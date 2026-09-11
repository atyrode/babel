---
id: babel-triages-the-queue
version: 1
kind: meta
scope: [corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read]
default: false
---

# Babel triages the queue

> **Standing duty, off by default.** This recipe is never part of a default
> selection. It runs when the operator has authorized the review triage duty —
> `babel conductor configure --babel-triages-the-queue` — or when it is named
> explicitly, and its subject is the pile of Babel's own proposals that nobody
> has ruled on yet. Everything it produces is advice attached beside a record
> and addressed to the person about to decide. It records no disposition, and
> the surface it writes through cannot express one.

## Question

Of the proposals waiting for a ruling, which say the same thing as each other,
which is worth reading first, what is the case against acting on each, and
where is there a better version of one that nobody has written down?

The pile is the reason this recipe exists. A proposal arrives already argued
for — that is what a proposal is — and it arrives one at a time, so the person
reading it is asked to judge it against a corpus of prior records they cannot
hold in their head and against its neighbours in the same batch, which they
have not read yet. Everything needed to do better than that is already stored:
the proposals themselves, their supporting and conflicting material, the claims
and findings they rest on, the prior records that say something similar, and
the decline reasons an operator has already written against records like them.
Nothing here is new instrumentation. It is one pass over material that already
exists, done before the reading rather than during it.

**Advice, never disposition.** This is the binding constraint of this recipe
and the one under pressure every time it runs. The recipe may rank, cluster,
weigh and re-propose. It may not accept, reject, defer, or mark a duplicate,
and it may not do any of those things in effect by other means: a proposal
ranked last has not been declined, a cluster is not a merge, and a
counter-argument is not a veto. Whether Babel should ever hold autonomous
disposition is an open question the operator has explicitly not answered, and a
run that behaved as though it had been answered would have answered it. The
store enforces this rather than trusting it — the triage surface has no path to
a disposition and refuses a proposal a ruling has already been recorded
against — but the guidance has to want what the type system enforces, because
a recipe straining against its own boundary produces advice written to be
obeyed.

**The counter-argument is the deliverable.** A rank is cheap and a cluster is
mechanical; the case against acting is the thing nobody else in the pipeline
has written. It is required of every piece of advice, and a pass that cannot
argue against a proposal records no advice about it rather than recording a
rank with a shrug attached.

## Inclusion, exclusion, and ambiguity

Include a proposal that is waiting: current wording, no disposition recorded.
That set is the subject, and it is the whole subject. The locatable material is
the proposal's own payload — problem, outcome, impact, applicability,
uncertainty, risks, open questions, prerequisites and verification criteria;
its form, `consolidated` or `candidate`, and what it therefore rests on; the
findings and observations behind a consolidation with their evidence and
counter-evidence; the claim a remedy addresses and the competing remedies
already addressing it; prior records saying something similar, with the
dispositions and decline reasons written against them; and, for a proposal
shaped as a change to code, the repository at a pinned snapshot.

Include the pile as a pile. Which of these are the same idea, which of them
contradict each other, and which one of a cluster is the best-stated version —
those are questions about the set, and the set is only visible to something
that reads all of it at once. A reviewer working down an inbox sees each record
against the records they happen to remember.

Include the argument the proposal's own material makes against it. A
proposal's `conflicting` evidence, its stated risks, its open questions and its
uncertainty are the counter-argument its author already half-wrote and then
buried under the outcome. Surfacing it is not an accusation; it is reading the
record properly, and it is the most defensible counter-argument available
because every word of it is the record's own.

Include a better alternative where there is one, as a proposal. "Narrower
scope, same support" and "the same change stated as the smaller change that
would prove it" are real contributions and cheap to act on. An alternative is a
new record resting on exactly what the original rests on, so the two are peers
and the operator chooses; nothing edits the original, and a run that tried
would be refused by the store.

Exclude a proposal already ruled on. A ruling answered the question; advice
arriving afterwards reads as a second opinion about a decision, which is the
one thing this recipe has no standing to offer. If the ruling looks wrong, that
is a finding about the review process and belongs to the product-dimension
recipe, phrased as a mechanism and reviewed like anything else.

Exclude the mechanism. "These four proposals duplicate each other because
retrieval never searched the frontier" is a defect in the pipeline and is
`babel-improves-babel`'s subject. This recipe's output is "compare these four";
emitting the mechanism here would bury a durable finding in advice attached to
a record that is about to be disposed of, and emitting this recipe's advice
there would add four more records to the queue it was asked to shorten.

Exclude any claim about the operator, on the same terms every meta recipe here
does. Their prior rulings are evidence about records, and a decline reason is a
fact about an output rather than about the person who wrote it.

Exclude advice on the pass's own alternatives. A run that ranked its own
alternative first against the proposal it was offered instead of has written
advocacy, and an alternative arrives with the advice that explains it — that
advice is the whole of what the pass gets to say about it.

Ambiguity is the normal state of a rank. Reading order depends on what the
operator has time for, what they were already thinking about, and how much
each proposal costs to act on, and the record settles none of that. Where the
order is genuinely arbitrary, say so in the ranking's own words: "these three
are interchangeable; read whichever is cheapest to act on" is a more useful
sentence than a confident 1, 2, 3.

Search the frontier before advising. The same pile will be met on every draw
until the operator works through it, so a proposal already carrying this
recipe's advice is a proposal to leave alone unless something has changed:
another pass added a near-duplicate to its cluster, the repository moved under
a code-shaped proposal, or a prior record it should be compared against
appeared. Re-advising an unchanged record with the same argument is this
recipe's most available failure, and it costs the operator exactly the
attention the recipe exists to conserve.

## Sorting cues

- two proposals resting on the same finding whose outcomes restate each other,
  which is a cluster before it is anything else;
- a proposal whose own `conflicting` material or stated risk answers its
  outcome, which is a counter-argument already written and not yet read;
- a proposal whose prerequisites name something the corpus shows does not
  exist, and which is therefore not actionable yet whatever its merit;
- a code-shaped proposal whose change the pinned snapshot already contains;
- a near-verbatim restatement of a proposal an operator declined, where the
  decline reason applies unchanged — the strongest counter-argument in the
  store, because it is the operator's own;
- competing remedies against one claim, none of which names the others, which
  is a decision about which remedy rather than four decisions about four;
- a `candidate` proposal rendered beside consolidations, which is a difference
  in what they rest on and therefore in how cheaply each can be believed;
- a proposal whose outcome is large and whose verification criteria are absent,
  where the alternative worth writing is the smallest version that would settle
  the question;
- a proposal waiting far longer than its neighbours, which is a fact about the
  queue rather than about the record, and belongs in the ranking's reasoning
  rather than in the rank.

Weak cues: how long a proposal is, how confidently it is worded, its stated
impact grading, and the order the pile happened to arrive in. Retrieval rank
contributes nothing. A proposal's own impact claim is the least reliable field
it carries, because it is the field its author had the most reason to inflate.

## Evidence and counter-evidence

Evidence, quoted with locators, always:

- the proposals clustered together, quoted at the wording that makes them the
  same idea — a cluster asserted without the two sentences side by side is a
  guess about vocabulary;
- for the counter-argument, whichever of these carries it: the record's own
  conflicting material, risk, open question or uncertainty, quoted; the prior
  decline reason, verbatim, with the record it was written against; the
  repository state at the pinned snapshot showing the change already exists;
  the prerequisite the corpus does not support;
- for the ranking, what each proposal rests on and how much: the findings and
  their observations' evidence for a consolidation, the addressed claim alone
  for a candidate;
- for an alternative, the material it rests on, which is the original's and is
  never widened — an alternative that quietly claimed more support than the
  record it improves would be the same epistemic failure the two proposal forms
  exist to prevent.

Counter-evidence to seek, and this recipe must seek it against its own advice
rather than against the records:

- for every cluster, the difference: two proposals sharing their vocabulary may
  propose opposite actions, and the sentence that distinguishes them is the one
  the clustering will have skipped;
- for every counter-argument, the proposal's answer to it — frequently the
  record already addresses the objection in a field the pass did not read;
- for a "already declined before" argument, whether this wording addresses the
  stated reason. A re-proposal that answers the decline is the system working,
  and arguing against it with the old reason punishes exactly the behaviour the
  refinement path asks for;
- for a "this already exists" argument, whether the snapshot is the repository
  the proposal targets, and whether what exists is the thing proposed or
  something adjacent to it;
- for a ranking, the cost side: a proposal ranked first because it is
  well-evidenced may be the most expensive one in the pile to act on, and the
  reviewer's time is the resource being allocated.

Advice whose counter-evidence section says "none sought" is not usable here.
The subject is the pass's own judgement, and it is the only record in this
pipeline that reaches the operator without having been reviewed first.

## Temporal and present-reality checks

Advice is written about a pile that is moving, and it is read later than it is
written, so three checks precede every piece of it:

1. Is this proposal still unruled? A disposition recorded between the read and
   the write makes the advice retrospective, and the store refuses it. Check
   rather than discover: an advice pass that has to be refused has spent its
   reasoning on a record already answered.
2. Is this still the current wording? A proposal that has been revised has a
   descendant, and advice about a superseded draft argues against words nobody
   will read. Advise the head of the chain or nothing.
3. Does the code still look like this? Any counter-argument shaped as "the
   repository already does this" is checked against the pinned snapshot, and
   one that cannot be is not made. An unverifiable "this may already exist" is
   worse than silence: it gives a reviewer a reason to decline that nobody
   checked.

Use `still-applicable`, `resolved` (the proposal's outcome is already in the
tree), `historical` (the proposal targets something that no longer exists),
`contradicted` (a prior operator ruling says otherwise), and `unverifiable`.
Say which records and which snapshot the status was established from.

## Classifications and stopping conditions

- `duplicate-cluster` — two or more waiting proposals stating the same idea,
  quoted side by side, with the difference between them stated if there is one.
  An invitation to compare and never a merge;
- `superseded-in-fact` — a waiting proposal whose outcome another waiting
  proposal states more completely. Still two records, still two rulings;
- `already-in-the-tree` — the change is present at the pinned snapshot, with
  the location;
- `answered-before` — an operator has declined this argument already, with the
  reason verbatim and a statement of whether this wording addresses it;
- `internal-objection` — the record's own conflicting material, risk or open
  question answers its outcome;
- `not-yet-actionable` — a prerequisite the corpus does not support, which is a
  reason to wait rather than a reason to decline;
- `alternative-offered` — a better-stated proposal for the same material,
  minted as a record, with what makes it better;
- `no-objection-found` — the pass looked and has nothing against this one. An
  honest and valuable answer: it is the only thing in this vocabulary that
  tells a reviewer the record survived a hostile reading, and a pass that never
  returns it is a pass that is manufacturing objections;
- `read-first` and `read-later` — the ranking's two ends, each with its
  reasoning, and neither meaning anything about the merit of the record.

Stop when the counter-argument is located in a record rather than inferred,
the cluster's members are quoted side by side, and the present-state check is
done. Stop before offering an alternative that merely rewords: a second record
in the queue costs the operator a second reading, and the bar for adding one is
that the alternative would be acted on where the original would not. Stop when
the honest advice is `no-objection-found`. Stop, and say so, when the pile is
one proposal: a rank of one of one is not a ranking, and the pass's whole
contribution there is the counter-argument.

Never rule. Nothing in this recipe accepts, rejects, defers or marks a
duplicate, and nothing it writes causes one of those to be recorded. A run that
behaved as though its advice settled anything would have taken a decision the
operator has not delegated and has said out loud is not yet settled.

## Cross-session synthesis keys

Group by: the material a proposal rests on — the finding, or the claim a remedy
addresses; the outcome, normalized to what would change; the counter-argument's
own kind, from the classification list; the recipe and version that produced
the proposal; and the pass that wrote the advice.

Recurrence means something different here than in a lens, and reading it as the
same thing is the error to avoid. A cluster recurring across passes is not a
stronger cluster — it is the same unmerged pile met twice, and the second
sighting is a fact about the review backlog rather than about the records.
What does promote is a counter-argument that lands: an objection this recipe
raised, which the operator's own decline reason then repeated in their own
words, is evidence that the objection was a standard nobody had written down,
and that is a finding — for `babel-improves-babel`, phrased as a mechanism,
not for this recipe to keep.

Synthesis should cross-reference that recipe in the other direction too, and
this is where the loop closes on itself. This recipe reviews Babel's proposals;
`babel-improves-babel` reviews Babel's output, and this recipe's advice is
Babel's output. Whether the operator read in the order suggested, merged the
clusters, took the alternatives, or declined for reasons the advice never
raised is all recorded, and it is the material for deciding whether triage is
worth its cost. A recipe that advises the reviewer and is never itself reviewed
would be the only unreviewed opinion in the pipeline.

## Capability needs

- `corpus-search` is required and does most of the work. The waiting pile, the
  lineage behind each proposal, the prior records a candidate resembles and the
  decline reasons written against them are all retrievals, and the
  `"scope": "frontier"` search is what makes the cluster and the
  `answered-before` objection possible at all. Without it the pass can rank
  what it was handed and nothing more, which is the least valuable thing it
  does.
- `repo-read` at a pinned snapshot is required before any `already-in-the-tree`
  objection. It is the strongest counter-argument available for a code-shaped
  proposal and the one a reviewer is least able to check quickly themselves.
  Without a snapshot the objection is not made and the advice says so; a
  guessed "this may already exist" hands a reviewer a reason to decline that
  nobody verified.
- No `sandbox-exec`: nothing here runs anything. A proposal's verification
  criteria are the author's suggestion for a human, and executing them would be
  acting on a proposal this recipe exists to stop short of.
- No `public-research`: the material is entirely local, and an objection drawn
  from the open web would be an argument the operator cannot check against
  their own record.

Dispositions this recipe emits: none, and that is the recipe rather than an
omission. Its output is advice attached beside a proposal — the ranking, the
cluster, the counter-argument — and, where it has one, an alternative proposal
minted in the same transaction as the advice that explains it. Those are the
only two things it writes. It emits no `draft-issue`, no `ask-question`, no
`store-memory`, no `propose-reality-fact` and no `develop-further`: every one
of those is a record that would join the queue this recipe was asked to
prepare, and a triage pass that lengthens the pile has inverted its purpose. A
question the pass genuinely needs answered goes in the counter-argument, where
the operator is already reading.

## Known failure modes

- **Advocacy in triage's clothing.** The defining hazard. A rank with a reason
  written to be obeyed, a counter-argument for the proposals it wants declined
  and `no-objection-found` for the ones it likes, an alternative offered as a
  replacement rather than as a peer. The tell is asymmetry: every proposal gets
  a hostile reading, or the advice is advocacy.
- **Ranking as verdict.** Last is not rejected, and a reviewer who reads the
  order as a recommendation to decline the tail has been misled by this recipe
  whatever its wording said. Say what the rank means, every time.
- **Clustering by vocabulary.** Two proposals sharing their words may propose
  opposite actions. A cluster is asserted from the sentences, quoted, and never
  from term overlap.
- **Manufacturing objections.** Every proposal can be argued against by someone
  determined enough, and a pass that never returns `no-objection-found` has
  stopped discriminating. An objection nobody would act on is noise wearing the
  most authoritative shape this recipe can produce.
- **The alternative that is a reword.** A second record costs a second reading.
  If the operator would act on both identically, the alternative should not
  exist.
- **Re-advising an unchanged record.** The same pile is met on every draw. The
  second identical piece of advice is pure cost, and it accumulates fastest
  exactly when the operator is furthest behind.
- **Punishing a refinement.** A re-proposal that answers a prior decline is the
  loop working. Arguing against it with the decline it already addressed
  teaches the pipeline not to revise.
- **Crossing into the mechanism.** Why the queue is full of duplicates is a
  durable finding for another recipe. Said here it is advice on a record that
  is about to disappear from the queue, and the finding disappears with it.
- **Claiming authority.** Nothing here decides anything. The recipe ranks,
  clusters, argues and proposes; a person accepts, rejects, defers and merges,
  and the two are different records written by different hands.
- **Compliance as correctness.** Fully evidenced advice can still be the wrong
  reading of a pile, and the operator ignoring all of it is a legitimate
  outcome rather than a failure to be designed around.

## Examples

Useful advice on a two-proposal cluster: two consolidations wait, both resting
on the same finding about constraints restated after a change. The first
proposes "state constraints up front in the handoff document"; the second
proposes "require the handoff template to carry a constraints section". Both
outcomes are quoted side by side; the difference is stated — the first is a
practice and the second is a mechanism, and only the second can be checked.
Classification `duplicate-cluster`, with the second ranked first and the
reasoning saying why: it is the one whose verification criteria the record
actually supports. The counter-argument against the one ranked first is its
own, quoted from its conflicting material: one of its supporting observations
records a handoff where the constraint was stated up front and dropped anyway.
Neither is declined. Both stay in the queue, and the operator is told which
comparison to make first.

Useful advice with an alternative: a proposal asks for every recipe's
inclusion section to state an exclusion the records show three of them
enforcing informally. The counter-argument is the scope: the evidence covers
three recipes and the proposal covers fourteen, and the eleven it has no
evidence for are where it would be wrong. The alternative is minted against the
same finding — the same exclusion, stated in the three recipes the evidence
covers, with the wider change named as what a later pass could propose if those
three hold. Classification `alternative-offered`. Two records, two rulings, the
original untouched, and the advice on both says which is which.

Advice correctly withheld: a proposal about run receipts looked like a
restatement of one declined last month, and the decline reason was "no evidence
that the receipt is where the information was lost". The counter-evidence check
read the new record: it cites two receipts where the denied tool request is the
only trace of the failure, which is exactly the evidence the decline asked for.
Classification `no-objection-found`, recorded with the reasoning, because the
reviewer reading this record needs to know that the obvious objection was
checked and does not apply.

An error to avoid: "three of the five waiting proposals are low-impact; suggest
declining them to clear the backlog." It reads an impact grading the proposal's
own author assigned as evidence, it recommends a disposition, and its subject
is the length of the queue rather than the merit of anything in it. The valid
version of the same observation is a ranking with its reasoning, and — if the
backlog itself is the problem — a mechanism finding for the recipe whose
subject that is.
