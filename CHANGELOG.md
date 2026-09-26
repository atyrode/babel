# Changelog

All notable changes to Babel are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/) with a `0.x` pre-stability series.

Entries up to v0.1.0 reference commit hashes; development is PR-based from
2026-08-28 onward, so later entries reference pull-request numbers.

## [Unreleased]

### Added

- **The machine half can catalogue the archive, and every restic read runs without a lock.**
  `babel/machine/catalog.ts` lists the snapshots tagged exactly `babel` (never the hub store's
  `babel-store` backup) and writes one session row per capture, newest per label, with times
  normalized to UTC. Chain heads are listed first, `maxSnapshots` bounds a run, and a memory in
  the managed cache carries the rest to the next run. The receipt reports per-label counts and
  the output lease's capacity. Recall and the catalog now share one listing rule
  (`babel/machine/archive-listing.ts`). `cat`, `snapshots`, `check`, `ls`, `dump` and `restore`
  run with `--no-lock`, while `init` and `backup` keep restic's lock. Tests against a synthetic
  repository cover chain order, the cap, the memory, the tag exclusion, offset times, and a
  restic killed mid-listing leaving no lock (#453).

- **The hub selects archived captures and ingests the catalog's rows.** A preparation is handed
  the exact captures it reads, grouped by snapshot with each path, size and modification time,
  instead of selectors. `launch`, the conductor's analysis draw and the title lane select only
  sessions whose row names an archived capture, under every host label rather than the launch
  machine's, and "recent" is when a session was last written rather than when it was catalogued.
  The input stops before `PREPARE_INPUT_MAX_BYTES`, and the material is bounded by the machine's
  newest `outputCapacity` less `MATERIAL_HEADROOM_BYTES`, divided by the lane's concurrency (one
  launch, the per-machine bound, a drain's fan); both bounds count what they leave in
  `overBound`. `sessions.json` rows are parsed against `SessionRowSchema` and written through
  `upsertSessionRows`, and a row naming no capture is refused by name. The beat and the default
  plan follow `keep-going`'s operation, `verify` runs on any machine and lists the catalogued
  path, and a titling run whose preparation read every recorded title settles with no Code
  session. Regressions cover selection, both bounds, the drain share, ingestion and the title
  lane (#453).

- **Watch names the archive labels no machine answers for, and a retired `scan` run keeps its
  name.** The `pulse` door's answer gains `archive`: each host label the catalog filed sessions
  under that no `archive_labels` row maps, with its session count, most first, at most 64 with
  the rest counted. Watch renders it as one Archive line under the last cycle, from the same read,
  and nothing when every label is mapped; those sessions are still selected and prepared, and
  `rehostSessions` is what maps a label. `RETIRED_OPERATIONS` names `atyrode.babel.scan`, so a
  historic scan receipt reads as "Scan (retired)" rather than a bare id. The specification and
  the building, runbook, parity and threat-model documents state the archive-first contract:
  collection on the machine that holds the sessions, the `catalog` beat, preparation from
  archived captures on any machine holding the archive binding with host network, lock-free
  reads, label mapping and the store's `babel-store` backup. Regressions cover the pulse's
  mapping, ordering and bound, and the rendered line (#453).

- **Catalogued sessions record their archive capture, and `rehostSessions` maps restic host
  labels.** The store moves to data version 1.12. Each session gains `archive_label` and
  `archive_path` beside its snapshot, and a new `archive_labels` table holds the operator's
  mapping of restic host labels to hub machine ids. `rehostSessions` records that mapping in the
  same transaction as the move and reports `labelled`, so later captures under the label are
  hosted on that machine. A label may map to itself. The capture-aware upsert
  (`babel/store/sessions.ts`) keeps a session on its newest capture and keeps the digest of an
  unchanged observation. The contract now fixes the capture, `prepare`/`catalog` input, session
  row and receipt shapes the archive slices implement. Regressions cover the upsert rules, label
  mapping and the 1.12 migration (#453).

- **Zero autonomous activity weights also stop auxiliary work.** The conductor withdraws its
  scan beat and posts no new title-generation preparation. Explicit explorations and drains keep
  their routed recipes and ordinary admission checks; already admitted work still reconciles.
  Regressions cover beat withdrawal, retained preparation and manual-only exploration (#264).

- **The Code/omp closure includes optional gateway request bounds.** Reviewed configuration can
  cap provider attempts and output tokens per call, including SDK retries and credential replay.
  The dependency gate and compiled worker verification pass; a deployed spending bound still
  requires matching runtime approval, explicit prices and job limits (#264, atyrode/code#170).

- **Disabling policy fences deferred model admission.** An ordinary exploration whose material
  finished preparing cannot buy its Code session under a disabled policy. Activation is checked
  atomically with the posting claim, so disablement during preparation cannot slip through a
  stale check. An unclaimed intent remains resumable after re-enablement; already claimed or
  uncertain work keeps its existing accounting fence. Regressions cover both disablement
  boundaries and exactly-once resumption (#444).

- **Bounded drains retain admission and inference limits across wakes.** `maxJobs` caps total
  admitted ordinals, not just concurrency; refused attempts consume a slot, and reaching the cap
  waits for held jobs instead of refilling. Launch and drain preparation preserve Code's reviewed
  `inferenceLimits`. Unconfirmed posts stay fenced rather than being mistaken for safe retries.
  Native settled inference totals now account for exploration, review and title jobs, including
  charged failures without transcripts; missing meters remain unknown. Live cumulative usage and
  retained progress come through Code's provenance-checked follow door, never from process
  existence or a fabricated one-call count. These source changes do not establish the live
  rehearsal or its shared spending envelope (#264, atyrode/code#170).

- **Native model progress keeps the current turn's clock.** Code's native worker reports
  redacted phases from the actual published one-shot event stream. Babel keeps the owner's
  observation time when intermediate tool phases are coalesced away, without restarting the
  clock on repeated snapshots or marking a new turn stalled because of an older call.
  Deployed readiness and the shared-cost live rehearsal remain separate acceptance (#264).

- **Recall brings bounded archived evidence to any agent, without lending it the archive.**
  An owner-managed native service fixes disclosure classes; search and locator windows share
  preparation's redaction, record identities and UTF-8 bounds. Whole sessions need a size preview
  before explicit sequential widening, with honest coverage, cost and derived traces. A versioned
  skill and exact SDK approval profile expose the same read contract without credential discovery.
  Shared preparation regressions and a source-deleted synthetic restic run reconstruct 245 pages
  byte-for-byte, preserve class isolation after cache warming, and confirm native cleanup. Live
  provisioning, managed activation and real-corpus disclosure remain operator steps (#156).

- **The dependency closure carries the reviewed bounded-result SDK.** Babel's SDK and both
  workflow gates now follow the same merged Manifold revision, with Code and omp advanced in
  dependency order. Agent-facing data remains explicitly approved and bounded; this supplies
  Recall's transport prerequisite, not Recall itself or permission to disclose an archive.
  The full frozen gate passes 1,060 tests, and a disposable engine installs all eleven bundles,
  dispatches their doors and verifies Babel's store creation and purge.

- **A long session record has the same boundary however its bytes arrive.** Preparation now
  segments oversized records in content order, preserving Unicode pairs rather than letting a
  filesystem chunk decide the normalized stream. Preparation identity advances to schema 3, so
  earlier cached readings cannot be mistaken for the corrected form. Chunk-boundary regressions
  and an isolated real-restic exercise prove unchanged raw capture identity and identical
  redacted bytes, source digests and record locators through live preparation and archive replay.

- **A refused next-action decision leaves no ruling behind.** A malformed historical record
  reference is rejected before the append or its event, rather than failing response validation
  after the decision was already written. Strict identifiers and historical rows stay unchanged.
  A real-store door regression catches the old ordering; an isolated exercise proves both first
  decisions and reconsiderations refuse without mutation while valid decisions still append (#437).

- **Preparation can choose what a session says, not only when it happened.** An opt-in content
  query on the existing machine operation selects bounded lexical matches from a local session
  index, then seals their exact selectors and digests into ordinary material. Indexing is paid
  once per changed eligible observation, always from redacted content; live and known own-run
  paths are excluded before reading. Empty results never widen the scope, incomplete coverage
  refuses explicitly, and index contention cannot break an ordinary selector preparation.

  Native SQLite regressions exercise concurrent builders, rollback and bounded locks. An isolated
  machine-operation exercise selects an older relevant session over a newer unrelated one,
  reuses unchanged coverage, replaces changed terms and verifies exact material identity.
  This adds neither an automatic review corpus read nor a new panel, preset or Recall API (#436).

- **Waiting is not a reason to keep an idea at the front.** Desk Next now fades from the first
  citation of independently sourced evidence or an explicit operator act. Repeated analysis,
  copied links, changed digests and passive reading buy no fresh attention; missing history is
  visibly unknown instead of borrowing an import date. The shelf keeps its evidence and opens
  at Top over all time, with no attention decay, and every list states the rule it used.

  Real-store regressions and a connected SQLite exercise prove that new sources and explicit
  acts renew attention without changing standing. Rendered preview interaction opens an old
  record without moving it, posts a comment that does move it, and returns to the unchanged
  shelf. Repair sweeps and copied correction links cannot manufacture renewal. Cursor paging
  keeps the full history: a 65,818-record synthetic rebuild fell from a median 872 ms to 369 ms,
  without dropping old citations. No provider call or production install is part of this proof
  (#435).

- **Challenge and synthesis get their own turns, not a tax on every exploration.** The conductor
  draws review, explore, challenge and synthesize by explicit relative weights, with the existing
  claims, ceilings and per-machine slots paying for all of them. Preparation keeps the same fenced
  claim through the Code session; a failed bind cannot turn uncertain live work into a free slot,
  and terminal cost is accounted once. Every new activity defaults to zero.

  A challenger writes objections with named grounds, and the candidate shows the objection count,
  distinct source runs and links to the objection and receipt rather than treating a review vote
  as criticism. Synthesis receives a bounded connected brief from multiple known source runs and
  keeps the original observation ids in its support edges. Invented or unoffered durable references
  are refused; neither stage writes rulings. The stage, lease and scope regressions and a connected
  synthetic SQLite-frontier exercise prove those boundaries. The rendered preview follows an
  objection to its record and source receipt; Watch's lower sections, including the weights, are
  reachable by ordinary scrolling inside their tile. No provider call or production install is
  part of that proof (#434).

- **Babel can find its own records.** Nothing indexed the corpus, so nothing retrieved against it:
  a preparation selected sessions by recency, the feed enumerated structured columns and accepted
  no text query at all, and the audit's own verdict — that the binding constraint is not ranking
  but that most records do not say what they are about — could not even be measured at scale. A
  `search` door answers keyword **and** meaning, fused by reciprocal rank because bm25 is
  unbounded and negative while cosine is bounded, with both raw numbers travelling beside the
  answer.

  **Two host facts chose the data structure, not taste.** A plugin cannot `load_extension`, so
  there is no vector extension below the SQL boundary — `vec0` does not exist. And a SQL call is
  bounded at 4 MiB while 6,038 records at 768 float32 dimensions is 18.5 MB, so a brute-force scan
  over full vectors **cannot cross the plugin's own boundary**. The index therefore keeps a
  one-bit-per-dimension sketch — 96 bytes a record, 580 KB for the whole corpus, one query — and
  rescores a bounded slice exactly. FTS5 turned out to be compiled in, so the keyword half needed
  nothing new.

  **The baseline now declares `services:invoke`, and that is the line to read twice.** Babel's
  server half could previously reach nothing at all; embedding a *query* has to happen at door
  latency, which the machine half cannot do, so the one honest mechanism is the service binding
  the judgement part already uses — the host resolves the credential and the key never enters this
  bundle. What leaves is a record's prose, capped, in one named field: no id, no run, no locator,
  no instant, asserted against the request's own bytes. **With no policy installed the baseline
  makes no outbound call at all**, which a test asserts by counting invocations rather than
  reading a return value.

  Partial is usable and says which: coverage travels with every answer, `absent`/`partial`/`full`
  names the state, and an approximation is separable from a miss — `scanned` of zero means nothing
  to compare against, while `scanned > rescored` means the sketch cut and a better match may lie
  outside the slice. A vector carries the model that made it and **cannot exist without one**, so
  a query is never compared against another model's vectors; those rows report `stale`, which is
  what a model change should mean. The backfill is a drain duty with no cursor — the pending set
  is the gap itself, so a dead tick, a restarted hub and a stopped drain all leave the same state,
  and a pass with nothing pending makes no call. The keyword half runs regardless of any account,
  so the imported corpus is searchable with zero configuration.
- **One narrow door, through which an allowed plugin may suggest and nothing more.** Babel has two
  writer classes — the operator, authenticated through a door under his principal, and a run,
  mediated by the conductor against a schema the baseline owns — and a dependent plugin is
  neither. Rather than admit a third writer to the append-only frontier, a plugin the operator has
  **named** may write one thing: a typed suggestion in `next_actions`, beside the record, which he
  accepts or declines. It cannot annotate a record, write an edge, change a standing, rule or
  delete, and the set of tables its path can reach is asserted rather than inspected. The rule the
  whole codebase already runs on — propose, never rule — now holds for a plugin too.

  **The host names no caller, which decided the design.** `IsolateDispatchCtxSchema` carries the
  trace, the principal, the capabilities and the scope, and nothing that says which plugin called.
  The caller *is* known host-side and reaches only the trace ledger and the cycle bound. So the
  door resolves a suggester from the authenticated principal through an allow-list on the policy
  document — the one thing the operator installs through a door, read back through another, every
  version kept with who set it and why, which is exactly the provenance a grant of write authority
  needs. It defaults empty: no plugin may write until he names one, and the same principal listed
  twice is refused, because otherwise attribution would depend on array order.

  Six refusals, each proved to bite by removing it: a forged author (the input has nowhere to name
  one — both schemas are strict, and the row's actor is a literal), an unlisted caller (refused by
  name, and told exactly what to add), a suggestion against a revision it did not read, one
  against wording since superseded, a record already ruled on, and the same suggestion twice —
  which is also the durable "already judged" mark a retroactive sweep needs, one constraint doing
  two jobs. A reading half sizes a sweep before it runs, because 6,038 unfiltered suggestions
  would be the one-list problem rebuilt inside the queue.
- **A blocker that closed is not a blocker, and now something notices.** T3 asks only that a
  blocked issue *name* what it waits on, so an issue could sit blocked for ever behind something
  that shipped — indistinguishable from abandoned, and the way a ready issue hides. T7 reports a
  blocked issue whose named blockers have all closed. It counts only an explicit `blocked by #N`
  in this repository, because an incidental mention is not a dependency and a blocker in another
  repository is one the script cannot judge; it fires only when **every** named blocker is spent,
  because one live blocker still blocks; and it reads open pull requests as well as open issues,
  since a blocker is as often one as the other and they share a numbering space — asking `gh
  issue list` alone would report every PR-blocked row as spent, which is the false positive that
  makes a new rule untrustworthy on the only run anybody grades it on.

  It found one on its first pass: #144, blocked by a #312 that closed hours earlier. That issue
  is now closed too — every anchor it rested on was deleted with the Go tree, and its substance
  was settled harder by the fix it was waiting for.
- **A service policy is composed and installed from a panel, and it names the file.** Babel binds
  host services and nothing in Babel composed one: installing the restic policy was a
  hand-written owner call spelled out in prose in the runbook. Two owner-only doors now preview
  and install, following the pattern `atyrode.code` already uses — the policy is composed from
  what the manifest's own `services` blocks declare, so a binding mismatch cannot be created by
  hand-editing one of the two; it is previewed with a digest; and it is applied as a
  compare-and-swap against the revision it was read at. A preview whose revision moved is
  refused, and so is one whose **composition** moved while the revision did not. Both non-owner
  refusals are proved to reach the hub **not at all** — the tests assert the configuration was
  never even read, because a version that asked and then caught would satisfy any assertion about
  its return value while reading something nobody authorised.

  **There is no key field, and there cannot be one.** Manifold has no path for a credential value
  anywhere: the agent reads it from a file on the machine, and the protocol says so — *"Native
  bootstrap advertises references and allowed origins, never source paths or values."* That gap
  is filed upstream. So the panel does the next best thing and **names the file**: the credential
  reference the policy wants, the path on that machine the agent will read it from, and whether
  the binding came up. Three separate facts — advertised, readable, and the remedy sentence — so
  that *not configured* and *configured and refused* stop producing identical silence, which is
  the failure mode "out of credit" was deliberately designed to look like.

  The proof that no value passes through Babel is on the same terms the call path already uses: a
  planted token in the environment, a full preview and install, and an assertion that neither the
  serialized preview nor the serialized host arguments contain it anywhere — plus that both input
  schemas reject a credential field outright, and that the policy's credential references are
  exactly the one name, read back through the engine's own function.
- **A Jev call has a size the part will pay for, and the same question is asked once.** The
  policy's ceilings meter spend per cycle and per day; neither bounds how large **one** call may
  be, and nothing stopped the same record being judged twice for two payments. A call is capped at
  8,192 bytes of serialized input — measured exactly as the host measures it, against the 65,536
  the protocol will carry, so the two numbers are comparable — and an oversized call is not sent,
  returning the same nothing every other absence returns rather than truncating the record being
  judged into a judgement of something the operator never sees. Bytes rather than tokens because
  the part holds no tokenizer and needs none: no token is shorter than a byte, so a byte cap
  bounds the bill from above.

  The memo keys on what actually determines an answer — the operation, the bank **and document**
  versions, which record kind was asked, the text verbatim, and the policy revision joined where
  the roster is read. What is *excluded* is the interesting half: not the record's id, its run or
  its recipe, because none of them is sent and keying on one pays twice for two records that say
  the same thing; not the clock, because nothing about a judgement decays and a day in the key is
  a cache that charges daily for one question; not the thresholds, because turning an answer into
  votes happens after the call and two operators drawing the line differently are asking the same
  question; and not the voter, because thirteen read one call. **An absence is never cached at
  all** — every absence is a state of the deployment rather than a fact about the question, and a
  cached unavailability would make one dry minute last until the process restarts.

  It is a bounded map in one module's scope and not a second store: 256 answers, least recently
  used evicted, tens of kilobytes however long the half lives. A bank version bump strands its
  entries unreachable, so the cache cannot outlive the wording it was computed under.
- **Jev's question bank is a versioned document, drift-checked the way a recipe is.** Four
  documents, one per record kind, with `version:` frontmatter registered in a manifest — the same
  shape `tools/seed-recipes.ts` already enforces for the cookbook, and for the same reason: an
  assessment cites `kind@version`, so a document whose wording moved without its version moving
  is a claim citing something that no longer exists. Two drifts are refused by name: a document
  disagreeing with the manifest, and a committed seed disagreeing with the documents.

  **A routing question cannot be tallied as a vote, structurally.** `subject`, `classification`
  and "contains instruction" route a record; they are not opinions about it. A routing entry has
  no threshold fields at all, so it does not typecheck as a vote — a `@ts-expect-error` holds
  that, which means the day it starts compiling the build fails. The parser closes both
  directions too: a threshold row naming a routing question is refused, and so is a routing row
  naming anything else.

  **A threshold with no observed distribution beside it is refused, not marked.** Every row
  carries the sample size and the share that fired, and a numeric cut additionally carries the
  mean and standard deviation it cuts at — because a threshold nobody can trace to a corpus is a
  threshold from somebody else's. "Admitted" is derived from the observed share rather than
  declared, so the flag cannot disagree with the number: nine sides sit in the documents as
  measurements and are excluded from voting, including two that fire on 0% of the observations
  they were written for. Deleting them would lose the evidence that retired them.

  Every document opens with the caveat the audit's §0 states: these distributions are one
  deployment's imported Go-era output, at one date, under one recipe and model set, and they are
  a reason to look rather than a target. Re-fit against what the plugin's own intake produces.
- **A session with no title in its log gets one, once, and it is marked as inferred.** The catalog
  has carried `title_provenance` to tell a read title from an inferred one since it shipped, with
  nothing to put in it, so a session whose log carried no title had none permanently and a listing
  of a few hundred read as a column of opaque selectors. It is inferred **as a Code session, like
  every other model call Babel makes** — there is no second path, because Babel binds no model
  service and holds no credential, and a titling lane with a route of its own would be Babel's
  first credential and a second egress with no disclosure surface. The retired product reached the
  same conclusion in its own words.

  **It is bounded the way any other spend is**, not by a background loop that quietly costs money:
  a disabled policy names nothing, a policy with no Code profile infers nothing rather than
  inferring for free, the daily and per-cycle ceilings both bite, one titling run is in flight
  deployment-wide, and a run offers at most twenty sessions — a batch the operator cannot finish
  reading is not a disclosure.

  **A read title always wins**, and two smaller rules make that hold. A scan that reads no title
  no longer erases one: the sessions upsert keeps the existing value, where before every scan
  wiped the inference and re-queued the session to be paid for again. And an answer arriving
  after a scan found a real title does not overwrite it. The ledger keeps its row either way, so
  what was paid for stays readable even when it is not what the listing shows — and a session is
  offered once, whatever happened, so a failed preparation cannot post another over the same
  batch on every wake for ever.

  One asymmetry is flagged rather than hidden: the review lane's admission reads the claims ledger
  only, so it does not see titling's dollars. With one titling run at a time the day's overshoot
  is bounded by one run's actual cost, and widening the claims table would have invalidated every
  stored policy.
- **Jev's key never touches the plugin.** The part declares one authority — invoking the
  judgement service the operator installed — and names that service by id; the host resolves the
  credential by reference and writes it into the outbound request. There is no field in the
  policy schema or in the call arguments where a credential *value* could be written, so the
  absence of the key is structural rather than a discipline anyone has to keep. The shape is
  `atyrode.babel.restic`'s, line for line, including the one thing deliberately **not** copied:
  restic's ambient binary fallback has no analogue for a credential, and adding one is the bug a
  test now defends against.

  **"Out of credit" stops being a policy and becomes a mechanism.** No binding, no call: the
  client reads the service roster first and answers nothing when no row is ready, having sent
  nothing and spent nothing. A revoked credential, an exhausted account and an absent part all
  arrive at the same return value along the same code path, rather than as three special cases
  beside each other — which is what every other Jev issue's fallback rests on, and why it had to
  be one path and not three.
- **Accepting a refinement writes the superseding revision, and a record renders out of Babel.**
  Two halves of the retired review service had no counterpart. A review could write a refinement
  naming the exact revision and JSON Pointer it would change, and nothing let the operator's
  acceptance of one **apply** it; and nothing rendered a record out of Babel at all, which is what
  "Babel drafts; the operator acts" rests on — a proposal the operator cannot carry anywhere is a
  proposal he has to retype.

  Accepting one now writes the new revision through the supersession path the operator's own acts
  already use, in the same transaction as the ruling, with the lineage on the edge. **A refinement
  written against a revision that has since been superseded is refused by name** — a refinement of
  an older wording is a refinement of something else — and the acceptance still stands, because a
  ruling is not un-appended by the failure of what it authorised. Two further refusals: a pointer
  at the record's identity rather than its wording, and a pointer resting on something that is not
  text.

  Two deliberate non-writes, both load-bearing. **No `status_events` row**: status is read per
  root, so marking the old revision superseded would have gapped out every review of the new head
  as replaced — the supersession would have silenced the wording it installed. And the superseded
  revision's outgoing edges **are** carried across, because a rewording is the same claim: a
  revision inheriting none would read as a finding resting on nothing and be reviewed with its
  evidence missing.

  The three projections §4.6 names — the issue draft, the agent brief, the operator note — are
  text and a filename. **There is no publish verb in any spelling**: the door asks for read
  authority and delegates nothing, so the power to reach anything outside this plugin's own rows
  is not held rather than merely unused. The issue draft and the agent brief are a proposal's
  alone, because a problem, a proposed outcome and acceptance criteria are a proposal's fields and
  an issue draft of a hypothesis would be a change request assembled out of a guess.
- **Every drain leaves a record of itself.** After the 2026-09-13 drain the questions the operator
  asked — did we hit a cap, what did it cost in tokens, how much erroring, how much value came out
  — had to be answered by hand, hours later, from receipts, `/proc`, fan logs and attempt rows.
  Nothing Babel produced could have told Babel that: the only durable trace was one receipt per
  job, with nothing relating them to the drain, the machine or the value produced. At every
  ending — target, deadline, stop, failure — the controller now writes one frontier record with
  `drain` provenance, keyed by the drain's own id so a second close writes no second record, and
  Watch shows the last one beside the panel.

  The relation that was missing was there all along: the controller **derives** each run's
  identity from the drain id, so the whole set is recomputable from durable rows at the end. The
  payload answers allocation as named and as spent, tokens and cost per duty and per account,
  jobs launched, at the model, settled, unsettled and refused **by code**, records and assessments
  per million tokens, the reasons for every gap, and the controller's own notes. It says out loud
  that per-duty figures **overlap** where one session performed several named methods, rather
  than leaving a reader to sum them and be wrong.

  **Three questions it cannot answer, named rather than filled with nulls.** The machine's CPU and
  memory: a plugin's server half reads no `/proc` and the hub reports neither, so what is reported
  is Babel's own fan — jobs held and jobs at the model, integrated, with peaks — labelled as
  Babel's own and not the host's. Cache-write tokens: the hub's frame carries cache reads only.
  And the account's window at the start and the end: Code owns the account and reports no window
  reading, so the report names the account and never a percentage.

  It is a record rather than a log because a drain is a session of Babel's own, and the report is
  eligible input to an `explore` run — so the next drain's changes can come from Babel's analysis
  of the last one rather than from a person reading receipts at midnight.
- **A run's calls are recoverable, by locator and never by copy.** Babel kept receipts — cost,
  tokens, model, closure — which is spend accounting, not traffic. So a claim about how Babel
  judged something could not be rechecked, only re-run, and a re-run is not the same event: the
  study that asked for this measured repeated identical requests returning byte-identical values
  **1 time in 16**. `run_calls` holds one row per call with what it cost and where its bytes are,
  and two runs can be diffed on what actually differed between them.

  **The finding that shaped it: the hub hands a plugin no per-call traffic at all, and for a run
  that reaches a model it hands Babel no per-call metering either.** The protocol's own inference
  event says so in its doc comment — the model, the tokens, the price, never a prompt or a byte of
  the answer — and Babel does not receive even that, because a Babel run is a Code session whose
  job belongs to another plugin. What a settlement is handed is a receipt: model, usage, exit
  code, and a final message bounded at sixteen thousand characters. The per-turn detail exists and
  is summed away before Babel sees it.

  So the row is one call as this deployment can name one, and the traffic is addressed rather than
  held: a digest of the answer, a byte count, and a locator to the sealed transcript. Two runs that
  answered byte-identically share one digest, which makes the 1-in-16 observable without keeping
  either copy. Cost is integer micro-dollars, the unit the hub meters in, so two runs subtract
  exactly rather than by float. An empty digest means no transcript was sealed, which is a
  different fact from an empty answer.
- **A lens that never looked at a topic can be pointed at it.** The `topic` door has answered
  `coverage` since it shipped — one row per recipe the policy declares, with the records filed
  under that topic reached through each, and the rows at zero were the point. A topic page read
  "never looked: Test economics", and reading a blank cell is not acting on one: nothing turned it
  into a run. Under the grid there is now a fold, **Point a lens that never looked here**, with a
  machine, a Code profile and one button per zero cell that could actually run.

  **It is the same launch Watch posts, and that is proven rather than asserted.** The request is
  assembled by one function both surfaces call, and a test builds the coverage cell's document and
  the Start form's document for the same machine, topic, lens and profile and requires them equal
  — the ceilings, the selection, the spend and the `machines:run` discharge are the door's, with
  no second route. `doors/launch.ts` is byte-identical; only its test grew. That comparison has to
  live in `test/`, because the lint boundary that landed with the Jev scaffold forbids one part
  importing another, and it refused the first version of the proof.

  A lens the policy has turned off is named in the prose and has no button, because the door would
  refuse it by name. A topic whose policy declares no recipe renders no control and reads nothing
  — an affordance for an empty set is worse than none.
- **A run can propose a typed next action, and only the operator answers it.** The retired product
  had five typed next actions a run could propose — draft an issue, propose a fact, store a
  memory, ask the operator, develop further — and an operator ledger of accept and decline over
  them. The plugin had nowhere to put one, and the crossing said so in its own words: the imported
  rows had no table to go to, and `machine/results.ts` documented the omission where the fields
  would be, because a schema field a run could fill and nothing could land would be worse than its
  absence. Two tables now hold them: the proposal, immutable, and the rulings, append-only, so a
  reconsideration stays readable rather than overwriting the first answer. The five words are the
  retired product's own, unchanged, so the stranded rows import as themselves with no mapping
  anyone has to re-check.

  **`plans`'s `CHECK` was not widened, and that is the finding worth keeping.** SQLite has no
  statement that widens one; the documented route is build-copy-drop-rename, and this store's
  additions are one statement each, applied only where the object they name is absent. A store an
  earlier enable created would keep the narrow constraint for ever while a fresh store got the
  wide one, and the two creation paths would stop producing the same shape. A new table is the
  only additive move the hook can make. The reason now lives beside the tables, because the next
  person to want a new `plans.kind` will reach for the `CHECK` first.

  **A run may propose and may never rule**, and that is structural rather than conventional. The
  set of tables a run's output can reach is a closed type, so an ingest entry naming a ledger does
  not compile; there is no output file that reaches one; `next_action_rulings` has **no
  actor-kind column at all**, so even raw SQL past the door cannot spell a run as the answerer;
  and the decision is its own door taking the operator from the principal, not a mode on an
  existing one. Four independent reasons, three of which hold without the fourth.
- **`atyrode.babel.jev` exists as a part, empty and wired.** Seventeen issues assumed the
  directory was there and nothing created it, so the whole judgement tree was blocked on a
  prerequisite nobody had filed — and every one of them read ready at a glance, because each
  carried a well-formed fallback paragraph. The part now packs, composes and installs: a manifest
  declaring `atyrode.babel` as a required dependency, a server half with no actions, **no
  capabilities, no store, no panel, no tool grant and no credential.** Those are the children, and
  a scaffold that quietly acquires a capability because it will need one is how an optional part
  stops being optional.

  **The fallback is tested rather than asserted.** `test/optional-part.test.ts` seeds one real
  store and dispatches **every** reading door twice — once against a hub where the part answers,
  once against a hub that refuses any call to a plugin the baseline does not declare, which is the
  host's own rule — and requires the two answers to be equal. A second test pins the door roster
  to the table, so a door added later cannot opt out of the fallback. It was proved to fail:
  making one door call into the part turns the suite red with the host's `undeclared_dependency`
  refusal. A fallback nobody has run is not a fallback.

  **"A part is not a library" is now a lint rule instead of a sentence in a document.** It applies
  to all three parts, not only the new one — `feed` and `watch` were checked and comply, their
  only baseline import being `contract.ts`, which is where every id, door name and result schema
  is spelled once. A name shared is not a dependency; a reached-into `store/` or `server/` module
  is, and it is the kind that survives the part being disabled.

  Two things found while wiring it. `tsconfig.json`'s `include` is explicit, so a new top-level
  directory is invisible to `tsc`: a part whose server half is never typechecked is present rather
  than wired. And `release.yml` hands the preview receiver a **hand-maintained list of plugin
  ids** while everything around it globs, so a tag would have attached four bundles and delivered
  three, with the guard structurally unable to notice a bundle nobody asked for. The id is added
  and the list now says out loud that it is hand-maintained and why the order cannot simply be
  globbed.
- **Nothing scanned a preparation for secrets before a model read it. Something does now.** A
  credential pasted into a transcript years ago was sealed into a material lease and sent to the
  provider along with everything else; the disclosure boundary was the operator's choice of Code
  profile and nothing else. A deterministic scan — fourteen named classes, thirteen structural
  formats and one bounded entropy heuristic, no model call, no network — runs **inside** the pass
  that digests and seals, so what the source digest covers is what a model reads.

  **A marker, never a digest of the value.** A redacted span becomes
  `[[babel-redacted:<class>@<line>:<offset>+<length>]]`. The retired product wrote a truncated
  digest of the secret, which is a commitment to it, and it travelled to the provider with
  everything else. The locator resolves only on the machine that prepared the selection, through
  the same splitter and normalizer that numbered the records, so the marker and the file a reader
  opens cannot drift apart. The locator travels; the bytes do not.

  **The false-positive bound is the design, not a detail.** A scanner that redacts every digest,
  identifier and path makes the corpus useless and gets switched off, which is worse than not
  having one — so the heuristic is bounded before it is allowed to judge: labelled digests are
  rejected (Babel's own `prep-`, `run_` and `hyp_` ids were a real false positive the tests
  caught), so are paths, placeholders, template and environment references; three of four
  character classes are required; the guess is suppressed inside self-declared data so a
  credential cannot hide beside an embedded image; and a structural match always beats a guess.
  Each of the fourteen classes has a positive **and** a near-miss that must produce zero findings
  from any rule, and one test runs a realistic Babel-shaped record — uuid, digest, prep id,
  timestamps, inline image, absolute path — asserting byte-identical output.

  A refusal names classes and counts and never a value, and a test asserts the message and the
  whole serialized receipt are free of the secret. The report is on **every** prepare receipt
  including `off`, because an absent field must not read as clean: absent means no scan ran, and
  `mode: "off"` is how an unscanned corpus is told apart from a scanned clean one. The prompt
  tells the model what a marker is and that nothing may be inferred about what it hid — a
  redaction is evidence that something was there, not evidence of what.

  A clean corpus's source digest is byte-identical scanned or unscanned, so no preparation
  identity moves unless its bytes actually held a credential. `docs/sandbox-threat-model.md`'s
  residual 5 is gone and the residual that actually remains is written in its place: a credential
  in a format no rule names still travels, and the rule table is the claim.
- **A record says which codebase it concerns.** In the interface it was often unclear which project
  a hypothesis was about, and the join was available all along: record → its family → the cited
  session → the repository the catalog probed. The peel now carries it, and distinguishes **how it
  is known**. A repository is `observed` when git answered in that directory and Babel wrote down
  what it said; `named` when only a run's answer carries it, read out of a transcript, with
  nothing of Babel's ever standing in that checkout. Those are different epistemic claims and
  conflating them is the quiet error this whole thing is about.

  The descent is required rather than convenient: a hypothesis cites nothing, so its repository
  lives on the observations hanging off it, a finding's on what it consolidates, and a proposal's
  two hops out. Remotes are canonicalised on both sides before they are compared, because
  `git@github.com:atyrode/babel.git` measured raw against `github.com/atyrode/babel` would file a
  repository Babel probed as hearsay. A checkout that declares no origin names nothing a reader on
  another machine can act on, so the entry is absent rather than guessed.

  **The commit comes from the evidence, never from a probe.** Nothing in the plugin records one
  and nothing in the retired product did either — its `RepoFingerprint` declared `commit` and only
  ever wrote `branch`, from one adapter. The run is the one reader of the transcript bytes, so the
  commit arrives through the answer contract, shape-checked and never re-derived.

  Issue and PR references ship in one form only: a whole URL the run declares, shown only where
  its own host, owner and repository equal the remote the record names. No `#N`, no `Closes #N`,
  no `gh pr create` — a bare number names a number in whatever project the reader assumes, and a
  link that looks precise and points at another repository's issue is worse than no link.
- **A cycle that did nothing says why.** The conductor's `TickReport` has carried a stop reason and
  a list of gaps since it shipped and no door exposed either, so a cycle that produced nothing and
  said nothing was indistinguishable, from every surface, from a cycle that was broken. The
  `pulse` door now answers the last cycle's stop with its detail and its gaps **counted by
  reason**, and Watch renders them under Runs, where they explain the absence. Counted, not
  listed: a loop contending with a second conductor declines every candidate it looks at, and
  shipping four hundred rows to a panel replaces an invisible loop with an unreadable one. Each
  counted row keeps the first instance's record and the coordinator's own sentence, so the panel
  reads `412 × claimed / another worker holds the claim / hyp_00000009 — …`: how much, what it
  means, and where to look.

  **A cycle that spent normally renders nothing** — no heading, no empty section, no "no problems"
  line — and so does a deployment whose loop has never run. The commonest silence is now named:
  a policy that is not enabled returns before the coordinator is ever reached, so the loop states
  that stop itself rather than leaving "why is nothing running" unanswered.

  `GAP_REASONS` and `STOP_REASONS` moved from the coordinator into the contract, word for word, so
  a door can spell a reason as an enum and refuse a word nobody declared. Watch's label tables are
  `Record<StopReason, string>` and `Record<GapReason, string>`: a nineteenth reason with no label
  is a compile error rather than a blank cell on a panel. A stored verdict from a build that
  spelled its reasons differently answers `null` rather than refusing the whole pulse — losing the
  explanation is the right loss; losing today's counts off Home is not.
- **What the operator told Babel reaches the run.** `tell` has written `steering` rows since it
  shipped and the `policy` door reads them back, which fixed the worse half — a box that accepts a
  sentence and shows it nowhere. The remaining half is what makes it a memory rather than a log:
  he could tell Babel to stop proposing work on a subject and the next run proposed it again. A
  run's prompt now carries his standing remarks and the remarks about the records in its own
  brief, **bounded** at eight remarks and two thousand characters, specific before standing and
  newest first. A remark that does not fit is skipped whole and the next considered, so one long
  remark cannot starve the short ones behind it, and nothing is truncated — half a sentence he
  wrote is a different sentence.

  **A remark is quoted evidence, never an instruction**, and it gets the boundary the prompt
  already gives untrusted material rather than a second one invented for it: uncitable, no
  locator, not under the material root, so a claim resting on one is the same refusal as any other
  unserved citation. The section is composed last, after the material and the prior records, so
  nothing he said sits among the sentences the model reads as its own contract — and a remark
  containing a `## How to answer` heading renders on one line, which a test pins by counting the
  headings in the composed prompt.

  The receipt names **which** remarks were carried and how many the bound left out, so a claim can
  be read against what the run was told and "told one thing" is distinguishable from "told one of
  four". It travels in the preparation document the run already carries: no new column.
- **A record says at creation whether everything it rests on came out of one run.** Corroboration
  was computed at read time and nowhere persisted, so nothing could rank, filter or route on it —
  a finding whose three supports are three readings of one run looked, to every query, like one
  with three independent ones. The payload now carries `restsOnOneRunAtCreation`, decided from
  exactly what the settlement can see: a support it minted itself carries this run's id, a support
  named by a durable identifier came from an earlier one. Where that is not enough to reproduce
  the store's own count — several earlier records and none of this run's — it declines to answer
  rather than guess, and `corroborationOf` stays the live authority. Two definitions of "rests on
  one run" that disagreed would be worse than none.

  **It marks and never refuses**, and the function's own comment says why to the next person
  tempted to make it a validation: in this deployment's imported corpus 175 of 207 findings rest
  on a single run and every one of its 116 proposals shares its finding's run, so a rule
  rejecting the shape would reject most of a corpus that predates the rule. Weak independence is
  not invalidity.

  The name carries the promise. Records are immutable by trigger and a correction is a
  supersession, so a support set is fixed at creation and the value cannot go stale — but a field
  promising a *current* number would be promising something the table forbids keeping, so it
  promises what it knows. Both creation paths now go through one row builder whose `supports` is
  required, so a third path cannot compile without stating what its record rests on: absence has
  exactly one meaning, which is "ask the store".
- **Three surfaces instead of one list, two axes instead of one label, and a concept holds one
  slot.** Babel produced one ordering of everything, so a record needing the operator's judgement
  competed with a record an agent could execute unattended and with a record worth keeping but not
  worth showing — and the first was buried by the volume of the third. The feed now **routes
  before it ranks**, by properties that were already columns:

  - **the desk**, what awaits his ruling, and the default;
  - **the agent queue**, the two standings whose own words name work a run does next;
  - **the shelf**, everything kept without paying attention for it — unruled candidates, records
    already decided, finished questions, superseded wordings.

  Nothing is deleted and nothing is hidden: a shelf record is one press away and narrows by topic,
  kind and standing. The desk's size is reported on **every** surface, so "is this a plausible
  amount of work" is answerable while looking at the queue.

  **What a record is about and how well established it is are two axes now.** The subject was on
  the row and the status was nowhere, so the page could not tell "important and shaky" from
  "trivial and certain" — the distinction that decides what to do about either. `established`
  reads off the columns that already held it: a ruling settles, a split inside one role is
  contested, a surviving deduped vote is reviewed, and nothing is unsettled. The ruling wins over
  the split, because Babel votes and the operator rules. **The row did not grow**: the status
  badge took the slot the second topic chip held, and a maximal row renders the same eight facts
  it did before.

  **The desk groups by an existing key** — the topic a record is filed under, or the recipe that
  produced it — so a concept observed forty times occupies one slot rather than forty, with its
  records beneath it and the key stated on the group. Paging counts groups, so a large concept
  cannot crowd the page; a group states its true size rather than looking complete; a record no
  key groups is a group of one **at its own rank**, never a heap at the bottom. A record filed
  under several topics is grouped under exactly one, so it is never the face of two.

  Two things this deliberately does not do. **The grouping is qualified, not settled** — the study
  that proposed it built it to check it and reported the grouping *method* as the thing still to
  validate, and the code says so where the grouping lives. And **the shelf is reachable, not
  searchable**: text retrieval over the corpus does not exist and its design is an open question,
  so #351's "findable by search" is not met here and stays with #337.
- **Babel reads the archive back.** `machine/restic.ts` ran `init`, `backup` and `snapshots`: the
  whole writing half and none of the reading one, so Babel could fill an archive and neither verify
  one nor restore a session from it. An archive whose restore path lives only in an operator's head
  is an archive nobody has tested. `check` (structural or over every stored byte), `ls`, `dump` and
  `restore` join the wrapper, surfaced as a fourth machine operation and the `atyrode.babel.verify`
  door: it checks the repository, then restores one catalogued session from a named snapshot and
  compares what comes back against both the snapshot's own bytes and the digest `scan` recorded.
  The snapshot and the digest come out of `sessions`, never out of the request, so a verification
  cannot be aimed at something the deployment never archived. The password reaches restic exactly
  as it always did — `RESTIC_PASSWORD` in the child's environment, out of the job's own service
  binding, never argv, never a log, never a file — and `test/contract.test.ts` now holds both
  repository-touching operations to that one delivery in a single loop.

  **Never-delete stops being an absence and becomes a rule.** The verbs restic may be asked for are
  a closed set of eight, every invocation is built by the one function that admits a verb or
  throws, and a test pins the refusal of `forget`, `prune`, `repair` and `unlock`. A compromised
  `archive` could write a snapshot and a compromised `verify` could read one; neither has a verb
  that removes one, and a fifth destructive verb now costs a deliberate edit to a named list and a
  failing test rather than a moment's inattention. Snapshot ids and paths are validated before they
  reach argv, because both now come from a door's caller.

  `docs/sandbox-threat-model.md` §7 named "a fourth machine operation" as a condition that
  invalidates it, so the document was rewritten rather than reworded around: §3's table gains a
  `verify` row, the network property is restated as two of four with the substance kept (the two
  operations reading the most hostile bytes still have no route out), and a new property says a
  restore writes corpus bytes onto the machine and names the sandbox as what bounds where. Residual
  4 gains the third copy, and §7's spent condition is rewritten so it would have caught `verify`
  itself. `docs/runbook.md` §2, `docs/parity.md`'s `restic/` row, `SPEC.md` §6.1 and the
  `babel-cli` skill all said Babel could not read the archive; they say what it can, and keep the
  by-hand path for the cases it still owns — no hub, no catalog row, a whole snapshot, or a lock.
- **A record's own words about another record become an edge.** Records in the corpus open with
  explicit self-correction markers — `CONTRADICTS hyp_…`, `CITATION CORRECTION for o2 …` — and
  nothing turned one into a link, so a record that announced what it superseded was, to every
  reader and every query, unrelated to it. `exploreRows` now parses the marker a record's text
  opens with and writes a `contradicts` or `corrects` edge at creation, and
  `atyrode.babel/tools/link-corrections.ts` sweeps a store for the markers already in it. The
  grammar is deliberately narrow, because a false edge asserts a relationship nobody stated and
  nothing downstream records that an edge was inferred: the token must open the text, a target is
  letters-then-digits and never a bare word, a possessive is not a target (`CORRECTION of o12's
  citation handles` means o12; `CORRECTION of observation o1's sibling` does not mean o1, and the
  two are the same shape), and the target list must be contiguous. A marker naming a record
  nobody holds is dropped with a note; nothing refuses, because a claim about the corpus is not a
  claim about the answer's integrity.

  **What the corpus actually holds, counted rather than assumed:** of 6,038 imported records,
  eleven carry a marker. Two name durable record identifiers. The other nine name a run-local
  handle (`o2`, `o12`) or only prose, so the issue's premise — that a correction marker names the
  record it corrects — is false for nine of eleven. The sweep recovers three edges from two
  records and reports the rest as unrecoverable, with the reason printed by the tool itself:
  a handle resolves only while the settlement that coined it exists, and those runs settled long
  ago. That is why creation and sweep are two capabilities rather than one — every correction
  written from here on becomes an edge; the ones written before mostly cannot.
- **The tracker has a lifecycle, and six rules a script proves.** Babel's labels were leftovers
  of a product that no longer exists — `phase-b`, `spec-drift`, `audit-2026-08-30`, and
  `manifold-transition` bulk-applied to twenty-three issues most of which are not about it —
  with no state dimension at all, so nothing said whether an issue was ready for an agent, held
  for the operator, or waiting on something named. It now carries atyrode/manifold's model,
  adapted: `docs/TRIAGE.md` owns label meaning, intake, holds, claims and exit;
  `.github/labels.yml` is the inventory and `bun run labels` proves the live tracker matches it;
  `bun run triage` enforces T1–T6 hourly and on every issue event, writing only the two that are
  bookkeeping; `bun run dispatch` answers what may be picked up now. The areas are this family's
  halves rather than a monorepo's packages, and delivery stays in `AGENTS.md` instead of being
  restated, because Babel has one gate where Manifold has four CI boundaries.
  `scripts/triage-policy.test.ts` proves all six rules against constructed issues, including
  both sides of the fourteen-day boundary the live tracker cannot exercise on demand — a test
  the upstream implementation's own comment asks for and never got.
- **A record shows how many runs it rests on, not just how many supports it has.** Three
  observations under a finding read as corroboration; three from one run are one reading
  restated, and the page said only the count — while 175 of 207 findings in this deployment's
  corpus rest on a single run and every one of 116 proposals shares its finding's run. The
  `record` door answers `corroboration`, computed at read time from `records.run_id` and the
  typed edges, and the peel reads "three supports, from one run" at the evidence depth. No
  column, no migration.
- **A topic says which lenses have never looked at it.** The `topic` door answers `coverage`: one
  row per recipe the policy declares, with the records filed under that topic reached through
  each, and the rows at **zero** are the point — nothing could say "this method has produced
  nothing about this subject", so nothing could propose the pair. The lens is reached through a
  bounded walk over the typed edges rather than read off `records.recipe_id`, because only an
  observation carries a recipe: grouping the column directly would have reported zero for every
  lens that ever produced a finding, which is a false zero and worse than no grid.
- **The operator's steering is read back.** `tell` has written `steering` rows since it shipped
  and nothing ever read one; his own words went into a table no surface opened, which is worse
  than not offering the act. The `policy` door answers his recent remarks and Watch renders them
  beside the ceilings. Feeding a remark into a run's prompt — what would make it a memory rather
  than a log — is #331.
- **`docs/jev-case-study-audit.md`** records what the outside evaluation of Babel's 6,038-record
  corpus measured, which mechanism each finding indicts and whether it is built, three
  corrections a reader must not miss, the question-design rules that are constraints rather than
  work, the stability bounds every quoted percentage needs, and the disposition of every issue
  this pass touched. Eighteen issues were filed from it (#328–#345) and twenty-four closed.
- **Five gates, each watched to fail.** The repository had no linter, no formatter and no
  unused-code check: `gofmt -l .` was the only formatting gate and it died with the Go tree.
  `bun run check` is now the aggregate the CI workflow runs — a typecheck, ESLint and Prettier at
  the Manifold sibling's own versions and settings, `knip` reachability over the entry points the
  manifests declare, a checker that every backticked repository path and every `bun run <script>`
  in a tracked document exists, and two rules about tests: a test may not read a `.md` file,
  because prose is not a contract, and may not skip itself on an environment variable, because a
  lane that cannot run should be zero tests rather than a green run full of silent skips. Each
  was proven by breaking it: an unimported file, an unresolved import, a document naming a page
  that does not exist, a test reading `SPEC.md`, a test skipping itself, a `==`, and a
  misformatted line. The checkers have their own suites, whose fixtures are violations by
  construction. Two `react-hooks` rules warn rather than error, and `eslint.config.js` records
  the four real defects they found in the reading surface and why fixing render semantics does
  not belong in a tooling change.
- **An exploration now produces records, which Babel-as-a-plugin had never done.** A settled Code
  session used to be read, checked against the material it cited and turned into a receipt, and
  the hypotheses, observations, findings and proposals inside the answer went nowhere: every
  record the plugin held was imported Go-era history. The settlement now writes them —
  `records`, the `cites`/`consolidates`/`addresses` edges between them, a candidate's first
  status event, and the questions a run raised — through the same ingest a sealed machine output
  goes through, with the development path enforced and nothing at all written when the answer is
  refused. Every identifier is a digest of the run and the model's own handle, so a settlement
  replayed after a crash writes the rows once. A run still cannot write a ruling.
- **The cookbook's seventeen recipes live in the repository as plugin data.** `bun
  tools/seed-recipes.ts import <dir>` reads a cookbook-shaped directory into
  `plugins/atyrode.babel/store/recipes.seed.json` — each recipe's id, version, title, the line
  its question asks and its whole body — and `policy` prints that as the `review.recipes` block
  `setPolicy` takes. A body seeded under a version the manifest does not record is refused,
  because a claim cites `id@version`. The tool installs nothing.
- **`docs/parity.md` records what the standalone product could do and what the plugin can.** One
  row per retired subpackage, each present, absent by decision with the reason quoted, or absent
  with the issue that names it.
- **The conductor now dispatches governed review draws through a pinned Code profile.** An
  enabled policy carries its machine, Code profile and versioned role recipes, so Babel claims
  each draw before posting a blinded session and settles the fenced claim from Code's receipt
  instead of stopping at `draw_pending`. Review comments and refinements can address an exact
  JSON Pointer in the immutable record; a refinement becomes a separately reviewable proposal
  with its own votes and challenges, while a policy depth bound prevents recursive refinement
  from becoming an unbounded obligation. The routed conductor lifecycle, claim binding,
  granular validation and bounded refinement persistence are covered by the plugin tests.
- **Jev now screens a record and proposes work beside it, and can do nothing else to it.** An
  intake pass judges each record once for the whole document and asks every voter what that
  judgement means; the only thing a voter may return is a next action from the closed
  vocabulary, so no code path exists by which one could refuse, hide or demote a record. Every
  absence — the part removed, no service binding, no credit, a record too large to send — is one
  `null` and the record is reported NOT JUDGED YET rather than judged and found wanting, and a
  voter that throws is reported `failed` by voter and record while the sweep finishes. Three
  voters ship on it, each a line in a reviewable bank document rather than a constant: whether a
  record's confidence outruns its evidence (a score, because the yes/no form answered the same
  way for 92.4% of the corpus), what kind of check would settle its claim (a choice over six,
  the study's most robust axis at 37.5% settleable from Babel's own tables), and whether it says
  what to do at all (a score whose lower two levels hold 54.9% of the corpus). The part computes
  and does not deliver: `babel.suggest` (#412) declares `containers:write`, a cross-plugin call
  is graded against the caller's own ceiling, and declaring that capability to reach one door
  would open every ruling door at the same stroke, so the pass hands its suggestions to a
  caller-supplied function and makes no door call but the one that sizes a sweep. The host
  mechanism that would admit the single write is open as atyrode/manifold#770. Proved by
  `babel/jev/screen/pass.test.ts` and the three voter suites: an unbound host, a refused
  service and an over-cap record leave a record byte-identical and produce one identical
  report; an admitted row that advises `none` still proposes nothing; and a score of 0.7
  reaches the tally as 0.7, backing a voter that 0.69 does not.
- **Jev's independent votes are now a position a consumer can read, rather than one net number.**
  The bank already held the thirteen calibrated voters and a weightless `tally()`; nothing said
  what that sum meant when two voters backed a record and one objected, when a reply left a voter
  silent, or when Jev did not judge the record at all. A derived position now names `backed`,
  `objected` or `contested` before giving the sum, carries both sides by voter, and states the
  admitted roster, who was heard and who was silent. Missing judgement has no number; a measured
  abstention is a real zero; a voter handed an answer of the wrong shape is silent rather than
  agreement; and a voter that throws is reported beside the position and never counted into it.
  The position is recomputed from the record revision, bank and judgement and is never stored, so
  there is no second authority to disagree with those rows. A pass reports the count at every
  standing, and one-record reads return the same `unjudged` position whether Jev is unbound or was
  never installed. The measured corpus row still reproduces seven back, two object, naming
  `scope` and `editorial`; boundary tests pin a 2–1 disagreement separately from unanimous backing
  and extend the existing unrounded-score path through to the standing an operator would see.

- **Jev can grade existing records in bounded passes, and Feed can read the result.** A free
  `sweepPlan` sizes all live record kinds under the bank and service policy revisions; an explicit
  press walks batches of at most 24 without changing Babel's ranking or the operator's rulings.
  Contested readings lead with the disagreement, and reception names backers, objectors and
  silent voters. Observations open from the related-record strip in their own kind, without
  becoming posts or acquiring ruling controls.

  The part still cannot write. Suggestions are submitted separately by its allow-listed caller,
  after their count is visible; only those submissions are durable. Readings and the continuation
  last for the browser session, so a silent record can be offered again after the bounded memo
  loses it. A refused judgement stops further calls without discarding an earlier batch.
  Real-store tests preserve the frontier and ranked feed. The installed local preview proves the
  unbound path and observation navigation; synthetic browser responses exercise contested and
  unheard readings, partial progress and explicit submission without a paid provider call.

- **Jev can propose which pairs are worth asking about, then report two relations without
  confusing them.** Comparing the imported 6,038 records outright would be 18,225,703 pairs, so
  a bounded proposer rides the corpus index instead: each record is one search anchor, each
  unordered neighbour pair is proposed once, and the answer carries its ceiling, whether the
  ceiling cut, how many searches ran, and whether meaning was absent, partial or approximate.
  With no embedding policy it still proposes from FTS5, says that the meaning service did not
  answer and makes no invocation — the lexical floor the study itself measured, not silent
  success.

  Contradiction and supersession ride one paid pair judgement but remain different types. A
  contradiction is canonical and symmetric, is delivered beside both records in identical words
  and has nowhere to name a preferred side. A supersession names `stale` and `fresh`, carries both
  instants, reverses when the ordered input reverses, and delivers only beside the stale record,
  making direction part of the effect rather than a label. Neither invents a bank threshold:
  the measured 40 contradictions and 36 supersessions were over one lexically blocked
  2,000-pair sample, not records of a kind, so the caller must state a cut and an absent cut is
  reported as uncalibrated. Proved by `babel/jev/pairs/`: no-policy invocation count, observable
  proposal bound, symmetric contradiction delivery and direction-reversing supersession delivery.

  Those two relations are now reachable at runtime rather than only computable. `jev.pairs` is a
  third door beside the sweep's two: its caller names the anchors and the cuts this deployment
  has measured, the pass reads each anchor through `babel.record`, retrieves candidates through
  `babel.search`, and buys ONE judgement per ordered pair through a second service operation —
  `pair`, which carries two states where the per-record `judge` carries one. Reusing `judge`
  would have sent half a pair and read a relation off a projection that never names one, so
  `seed-questions.ts policy` now prints both operations' literals and the pair projection's two
  leaves. The report separates candidates, attempted, judged and truncation, so a deployment
  that installed half a policy reads nothing like a corpus with no contradictions in it; the
  door declares `containers:read` alone and the suggestions come back for the caller to deliver.
  Every suggestion's basis digests the service policy revision and the stated cuts alongside the
  question wording, and the revision is re-checked at the call, so an answer can never be filed
  under a policy that did not produce it.

- **Two findings about one record no longer overwrite each other.** `babel.suggest` kept one live
  suggestion per suggester, revision and kind, which is right for a per-record voter and a loss
  for a pair: a record that contradicts two others carried whichever was written second, and
  nothing said the first had been dropped. Optional `subject` and `aspect` fields distinguish the
  counterpart and the independent relation in both the live key and the insert guard. The
  counterpart must exist and cannot be the record itself. Empty defaults preserve existing
  per-record callers and rows. The door regression proves that different counterparts and
  different relations about the same counterpart survive in either order, while restating one
  replaces only that finding.

### Removed

- **The standalone Go product is gone.** `cmd/`, `internal/`, `web/`, `test/`, `cookbook/`,
  `go.mod`, `go.sum`, `flake.nix` and `flake.lock` are deleted: about 296,000 lines of a
  product Babel stopped being, against 61,000 lines of the plugin family it is. Nothing under
  `plugins/` ever imported any of it — every `internal/` mention there was a provenance
  comment, and each now names the tag `v0.4.0`, where the whole tree stays readable.
  `docs/parity.md` is the record of what the removal cost, row by row. One loss is worth
  naming here: the plugin fills the restic archive and cannot read it back, so verifying and
  restoring are `restic check` and `restic restore` directly, which
  `.omp/skills/babel-cli/SKILL.md` now states.
- **A `v*` tag no longer publishes a Go binary.** The release job's four-platform
  cross-compile is gone; a tag publishes the verified plugin bundles and their dependency
  closure, which is what the preview's receiver takes. Module versions already published stay
  resolvable through the proxy.

### Changed

- **`archive` reads the operator's session trees through read-only operator anchors, under the
  machine's own label.** Its locations move off the `home` anchor, which on a native worker is the
  service account's workload home, onto `operator.omp-sessions`, `operator.codex-home` and
  `operator.claude-home`, each named whole and read-only at unchanged guest paths, and
  `operator.omp-blobs` joins them so an archived OMP session restores with the blobs it
  references. No location in the manifest names `home` any more. The job's input gains a required
  `label`, which is restic's `--host` in place of the machine id: a machine's Manifold-made
  snapshots are filed under the label its collector already uses (`dev-01`), so one
  `archive_labels` mapping covers both. The label is the input's rather than the storage
  document's, which is custody and may serve the whole fleet. `test/contract.test.ts` pins the
  four anchors, read-only access by `archive` alone, no `home` location, and a mount for every
  backup root but the deferred `~/.omp/collab`; `babel/machine/archive.test.ts` backs up a
  job-shaped home with the adapters' own discovery and finds four per-root snapshots, blobs
  included, under the label. A machine that collects now needs the four anchors declared, bound
  and consented (`docs/runbook.md` §6) (#453).

- **`catalog` is the beat, `prepare` binds the archive, and `scan` is retired.** Analysis reads
  the fleet's restic archive and never a machine's own session files. The manifest declares
  `atyrode.babel.catalog` — restic, the `atyrode.babel.restic` storage binding, host network, its
  memory and restic's index in the managed cache, 1 GiB of output — and `keep-going` and the
  conductor's beat post it. `atyrode.babel.prepare` binds the same service with host network and
  mounts no `home` location. `atyrode.babel.scan` and `machine/scan.ts` are deleted, with the
  adapters' file-based discovery and description; the git observer only `scan` used
  (`machine/repository.ts`) is kept, by the operator's decision, with nothing but its own test
  importing it. `RETIRED_OPERATIONS` keeps its historic runs named, and Watch labels a catalog run
  "Catalog". `archive` stays the collector, with its `home` locations until a Manifold anchor
  replaces them, and writes an empty `sessions` document: the catalog lists its snapshots like
  any other `babel` snapshot, so captures have one writer. Every operation asks the owner for
  `restic` and none for `development`. `test/contract.test.ts` pins host network, the storage
  binding and restic on every operation, and a `home` anchor on `archive` alone; the packed
  bundle runs `catalog`, and the dispatcher runs `catalog` and `prepare`, against a synthetic
  archive (#453).

- **`prepare` reads the captures the hub selected out of the archive, and never a local file.**
  Its input is the contract's `PrepareInputSchema`: captures grouped by snapshot, each with its
  path, size and modification time. Each capture is streamed with `restic dump` into the single
  pass that normalizes, scans, digests and seals, and the redacted reading is kept per
  repository and label in the managed cache, so a second preparation over the same captures
  spawns no restic at all. A preparation refuses whole with a `PREPARE_REFUSALS` code:
  `material_bound` and `material_storage_insufficient` before anything is fetched,
  `capture_missing`, `capture_changed` and `archive_unavailable`. Each material entry names its
  `origin`, the selection's host is the capture's label, and the receipt reports `fetched`,
  `fetchedBytes` and `outputCapacity`. `sessions.json` rows name the capture read, with the
  snapshot's own time as `archived_at`, and carry the title, workspace and usage the pass read
  from the redacted stream (`babel/machine/session-facts.ts`, through Recall's metadata rule and the
  adapters' usage fold). `verify` lists only a catalogued `restore.path`, and `resolveRedaction`
  takes the capture's byte stream. Tests against a synthetic repository cover two labels in one
  material, a cache hit with no restic call, each refusal, redaction, the rows and own-run
  transcripts (#453).

- **The documents stop calling the store durable and `mkdir -p` a fix.** `AGENTS.md` and the
  README said a settled run's records were durable the instant they were written. The hub's
  store is a single copy: Manifold's `data.db.backup` is a same-volume migration rollback image
  and Litestream excludes plugin databases, so losing the hub's volume loses every record.
  `AGENTS.md`, the README, runbook §5 and the schema's header now keep it the one Babel-owned
  store, with no sync and no second store, and name its backup as open work with a decided
  destination: the session transcripts' restic repository under its own `babel-store` tag
  (#454). The `home` anchor guidance said creating the three session directories was the whole
  fix; on a native Manifold worker that anchor is the service account's workload home, so the
  jobs start and read an empty tree. It now says so and points at preparing from the archive
  (#453). And running Babel's ordinary work on any model, a free one included, is stated as
  ordinary operation: the runbook's §11 ceremony applies when the operator asks to drain a paid
  usage window.

- **The tree is `babel/`, and the judgement part lives inside it.** Two layout facts had drifted
  apart from what they meant. `atyrode/babel` contained `atyrode.babel/` — the organisation named
  twice and the product named twice — because the id-named directory made sense inside the
  `plugins/` wrapper that #326 deleted, and only the wrapper was removed. And
  `atyrode.babel.jev/` sat beside the baseline while `feed/` and `watch/` sat inside it, so the
  family had two conventions for the same kind of thing.

  A directory is named for its id's last segment now, and a part is a directory inside its
  parent's: `babel/feed`, `babel/watch`, `babel/jev`. Nothing about identity moves — `pack` reads
  each id from its own `manifest.json`, so no manifest id, no bundle name and no dependency edge
  changes.

  The placement had been quietly charging rent. `tsconfig.json` needed an explicit include for a
  top-level directory that nesting makes unnecessary; the import-boundary lint rule was written
  **twice**, once keyed by path depth for the nested parts and once by name for the sibling, and
  is now one rule covering all three; and `pack.sh`'s comment claimed parents are packed before
  parts when a sibling sorted ahead of its own parent. The part's imports of the baseline lost a
  path segment and stopped naming the baseline at all.

  One test was found **passing vacuously**: the scan proving nothing in the baseline imports the
  part walked a directory the rename had emptied. It walks the right tree now and was proved to
  bite by importing the part from the baseline and watching it go red.
- **The family is at 0.4.0.** The baseline stayed on 0.3.0 through a night that gave it a fourth
  machine operation, a secret preflight, correction edges, a steering memory, a corroboration
  determination, a repository on every record, a cycle's reasons on the pulse door and two new
  tables. Feed and Watch were a patch apart from each other for no reason either could name. All
  three go to 0.4.0 together, which is how they ship and how a hub installs them;
  `atyrode.babel.jev` stays at 0.1.0, because a part that has published nothing has not reached
  its first minor. Tagging a release remains the operator's act.
- **The Feed part's list panel is called Home, not Babel.** The plugin manager nests the parts
  under their parent, so the tree read `babel` → `Babel` → the one list, which says the part is
  the product rather than a view of it. The part itself has been titled `Feed` since the layout
  flattening; the panel had not caught up. It is `Home` now, which is what the part's own
  description has always called it, and the scroll region's accessible name matches. Feed goes to
  0.3.2; Watch is untouched, so it does not advance — a version that moves without a change in
  the bundle is a lie about the bundle.
- **The repository is the plugin family, and its layout now says so.** `plugins/atyrode.babel/`
  was two levels deep for a reason that stopped being true: the wrapper separated the plugin
  family from the Go product beside it, and that product is gone, so it separated the plugins
  from nothing and — because parts nest inside their parent — would have held exactly one child
  for ever. `plugins/` is removed: the workspace, the two revision pins, `pack.sh`, `scripts/`
  and `test/` sit at the repository root beside `atyrode.babel/`, which is what `pack` takes as
  a directory and must not contain the changelog or the workflows. The SDK sibling resolves one
  level shallower — `../manifold` in a developer checkout and in CI alike — and both workflows
  pass `plugins-dir: "."`. `plugins/README.md` becomes `docs/building.md`, which is what it was
  always about. The same wrapper is still correct in atyrode/code, where a Go product does sit
  beside it; atyrode/code#204 records that a sweep of that tree inherits this smell.

- **Every document describes only what Babel is, in the present tense.** `SPEC.md` keeps its name
  and its section numbers — so the `SPEC.md §N` citations in code and issues still resolve — and
  loses the standalone product, the phase plan, and §13's log of ninety numbered decisions: each
  surviving rule moved into the section that owns its subject, stated as behaviour with no number
  and no date, and the rest went with the code it described. Where a capability is designed and
  absent the section says so and points at `docs/parity.md`. `docs/` drops four superseded
  designs — the Manifold transition, the runs-interface design that shipped as Watch, a dated
  batch handoff, and the plan whose phases are issues — and rewrites the four that remain;
  `docs/postmortem-2026-09-13-drain.md` stays a postmortem, with only its two dangling paths
  repaired. `AGENTS.md`, `README.md` and `plugins/README.md` describe the plugin family, its
  gate, and where a change is proved.
- **A correction worth naming: the drawn-review lane works.** Three documents said it answered
  `draw_pending` and it has not since the conductor learned to dispatch a fenced review. What is
  actually owed there is evidence, not code: every rehearsal of that lane has been synthetic, so
  the documents now state that boundary instead of a defect.
- **The plugin gate now runs on every pull request and every push to `main`.** `ci.yml` built
  the Go binary and the React bundle and was the only check an ordinary PR had; with those
  trees gone it had nothing left to run. `manifold-plugins.yml` drops its `plugins/**` path
  filter instead, so a PR touching only a workflow, a document or the changelog is gated too,
  and the repository is never without a check.
- **The pinned Manifold and Code now carry the delegable machine read Babel's doors declare.**
  `plugins/MANIFOLD_REV` and both workflow `uses:` refs — the gate's and the release gate's, the
  second of which a previous move had left behind (#306) — name Manifold `743ee75a`
  (`0.17.0+22.g743ee75a`), and `plugins/CODE_REV` with `@atyrode/manifold-code` name Code
  `c6a9c264`. Four of Babel's doors declare `machines:read`, which only became delegable in
  atyrode/manifold#740; against the old pin the gate refused the bundle outright with
  `invalid delegated capabilities`, so nothing here could be verified. The new revision also
  answers a plugin holding no installation the pre-deployment projection instead of refusing
  `job_installation_absent` (atyrode/manifold#744), which is what
  `atyrode.omp.describeDestination` reports, and the Code revision brings the OMP family that
  declares the same read (atyrode/manifold-omp#45). One consequence reaches operators: a
  non-owner token that reads a machine through any of these doors must now hold `machines:read`
  as well as `machines:run`, because the native bridge intersects the caller's capabilities with
  the door's (atyrode/manifold#749). An owner key is unaffected.

### Fixed

- **Every door that posts Babel's own jobs is lent what the hub discharges them against.** Since
  the archive cutover, the beat, a preparation and a verification each write the outputs and
  cache locations and read `atyrode.babel.restic` over the host network. The hub admits a posting
  only when the door's delegates hold every requirement the operation declares, so on the
  integrated preview every cycle logged `the beat cannot be registered:
  job_capability_absent:locations:write`. A launch, a drain start and a verification would have
  been refused for `services:invoke` or `network:host` next. `pulse`, `runs`, `launch`,
  `drainStart`, `drainStatus` and `verify` now carry `POSTING_DELEGATES`: `locations:write`,
  `machines:run`, `network:host` and `services:invoke`. `launch` and `verify` no longer carry
  `locations:read`, because no operation Babel posts reads a location. The manifest's
  `capabilities` declares `network:host`, since a door may delegate only what its manifest
  declares. `server.test.ts` serves each door the slice the host would and derives each posting's
  requirements from the manifest. The beat registered behind `pulse` and the start of a drain
  both fail on the old delegates (#453).

- **A store created under the first drain shape can start a drain again.** #285 created
  `drains.session` as `TEXT NOT NULL` with no default. #291 replaced it with `profile`, but it
  left the column in stores that already had the table, and no insert has named it since. So
  every `drainStart` on such a store failed with `NOT NULL constraint failed: drains.session`.
  The integrated preview's store is one of them, and it holds no drain rows. The enable now
  applies `SCHEMA_RETIREMENTS`: where `drains.session` is still a column, a row's value moves into
  that row's `knobs` and the column is dropped, in the enable's one batch. A new store never had
  the column, so the data version does not move. A test builds the #285 table, enables the
  plugin and starts a drain through the real door. A second test shows a legacy row keeps its
  session.

- **The services composer admits the owner's loopback store.** A policy whose origin is plain
  `http:` on `127.0.0.1` or `[::1]` now composes with `allowLoopbackHttp: true`, the only
  plain-http origin Manifold's `ServicePolicySchema` admits, so
  `previewServices` with `http://127.0.0.1:7811` for `atyrode.babel.restic` previews and installs.
  Before, the flag was always `false`, and the preview was refused with the schema's bare
  "Invalid input". An `https:` origin composes exactly as before. Any other origin the hub would
  refuse, `http://localhost` included, is now refused by name. A test previews and installs a
  loopback origin.

- **A launch no longer requires the operation node it discharges nothing at.** `launch` has
  declared no governed requirement since #279, but its input still required `operation`, so a
  keep-going press typed as `{machineId, preset, minutes}` was refused `invalid_args` before the
  handler ran. The node is now optional and taken as the preset's own. A node that names another
  machine or operation is still refused by name. A door test launches keep-going without it.

- **The dependency closure follows Manifold `2ee760dd`.** `MANIFOLD_REV` and both workflow
  `uses:` refs move to atyrode/manifold `2ee760dd`. `CODE_REV` and `@atyrode/manifold-code` move
  to atyrode/code#220, whose omp closure is atyrode/manifold-omp#84, so all three name that
  one revision. That revision contains atyrode/manifold#843. Before it, a machine owner gave the
  hub 5 s to decide a native service call's authorization and then answered 403
  `service_unauthorized`, even when the hub allowed the call. Now the wait is bounded by the
  call's own deadline, and only a denial is a 403. That fix is owner-side: it reaches a machine
  with its Manifold agent, not with these bundles. The revision also validates exported bundle
  bytes in one linear pass (atyrode/manifold#845) and adds operator-declared read-only anchors
  (atyrode/manifold#842). The omp closure also brings atyrode/manifold-omp#82 and
  atyrode/manifold-omp#83: a one-shot's unset model roles stay on its configured model, and only
  its configured providers are registered and credentialed. There are no Babel source changes.
  All four Babel bundles, all four Code bundles and all three omp bundles have new digests. The
  machine stamp moves because `machine.js` bundles the changed SDK code. The full frozen gate
  passes 1,203 tests. A disposable engine installs all eleven bundles and dispatches their doors.

- **One runtime scratch size admits every Babel operation that writes it.** `scan`, `archive`
  and `verify` declared 64 MiB of output and `prepare` 512 MiB, all cut from the one tmpfs the
  `runtime` anchor mounts. Manifold refuses a job whose `outputBytes` is below that tmpfs's
  capacity (`bounded-output-storage-required`), so a machine sized to hold a material above
  64 MiB refused the other three. All four now declare 1 GiB, Manifold's per-job ceiling and
  what `atyrode.omp.session` declares, and the scratch a machine is sized to is spelled once as
  `RUNTIME_SCRATCH_BYTES`: 768 MiB with 10000 inodes, which leaves each job 256 MiB of stdio.
  `MAX_MATERIAL_BYTES` stays 448 MiB, under both the scratch and the session's 512 MiB
  `inputBytes`. The building guide and runbook §6 give the sizing rule and the operator step,
  bundle first and then scratch. The contract test holds every runtime-writing operation above
  the scratch and the material bound under it, and fails on the old declarations.

- **An abandoned review no longer stalls the conductor.** An abandonment withholds nothing
  (#259), so the draw offers the same record and role again, under the same assignment id,
  because the ordinal that names a review does not count abandonments. `claim` refused that id
  as finished, and the conductor stops a cycle at its first refused dispatch. Once the eligible
  pool thinned, every cycle drew the dead assignment first and launched nothing until the policy
  version changed. On the integrated preview, launches fell from 44 to about 1 per ten minutes.
  The draw now treats an abandoned review as free, and `claim` takes the abandoned epoch over at
  the next fence, exactly as it takes over an expired lease: the dead epoch keeps its charge on
  its own `~fence` row. A completed, skipped or failed claim is still finished. An abandoned
  analysis stays settled, as before: its identity is its context, and unchanged context never
  receives another paid sample. `coordinator.test.ts` drives the review path: abandon, redraw,
  two workers racing for fence 2 (one winner, one conflict), spend charged once per epoch. It
  fails without the change.

- **A Code-backed review runs the model its profile names, or does not run.** `CODE_REV` and
  `@atyrode/manifold-code` move to atyrode/code#219, whose omp closure carries
  atyrode/manifold-omp#81. A one-shot now starts with exactly its configured model in scope.
  Gateway discovery waits 60 s rather than 10 s, and a live-listed id with a thinking level keeps
  its reasoning. Before this, a slow gateway let a review configured for
  openrouter/stealth/space-bunny-alpha start on the machine default, and on the integrated
  preview it answered as anthropic/claude-opus-4-8. Babel's own bundles are byte-identical, and
  only the `atyrode.omp` and `atyrode.omp.gateway` bundles beneath them moved. The full frozen
  gate passes 1,193 tests. A disposable engine installs all eleven bundles and dispatches their
  doors.

- **Babel can post its own jobs again.** `launch`, `drainStart`, `verify` and the three doors a
  cycle follows (`pulse`, `runs`, `drainStatus`) now delegate `machines:run`, the capability
  `engine.jobs.execute` and `schedule` discharge a posting against. Without it the integrated
  preview refused every Babel job `authority_or_consent_refused` and every cycle logged `the beat
  cannot be registered: job_capability_absent:machines:run`, so no scan, explicit explore, drain
  slot, drain relaunch or analysis stage ever ran; only Code-posted reviews did. The host still
  intersects the delegate with the caller's own capabilities and requires the operator's
  version-bound consent at each operation node. The server regression drives a `pulse` through a
  job slice attenuated the way the host attenuates it and fails without the delegate (#448).

- **An explicit explore's preparation is posted under `prepare`'s own limits.** The operator's
  press and the drain planned an explore for the undeclared `atyrode.babel.explore`, fell back to
  `DEFAULT_LIMITS` (an hour, 2 GiB) and posted the preparation above `prepare`'s declared thirty
  minutes and 1 GiB, which the hub refused `limit_exceeded`. They now plan for the operation the
  press posts, as the conductor already did; the launch and drain regressions plan through
  `runPlan` over the shipped manifest and fail on the old plan (#449).

- **Old malformed identifiers no longer poison a whole search.** Retrieval excludes unnameable
  records before its candidate limit, reports their count in coverage, and keeps topics and
  pending-suggestion reads usable without weakening the import guard. The regression includes
  more malformed matches than the candidate window and an embedded NUL; the installed preview
  returns valid results while reporting twelve legacy rows it cannot name.
- **The preview loop discovers this family, not its downloaded dependencies.** After dependency
  preparation, scanning the repository root tried to install duplicate Code bundles. `bun run dev`
  now scans `babel/`; the local preview installed all four plugins after the change.
- **Packing leaves the stamped manifest ready for the same checks as a clean checkout.** The
  stamper uses the repository's formatter instead of rewriting the file in generic JSON layout;
  packing followed immediately by the format check now succeeds, rather than failing the next CI
  run after a successful local gate.

- **A record could be imported that no door would ever open, and nothing could remove it.** Found
  by rendering the feed on a hub rather than in a test document: twelve rows went in through the
  crossing, the feed listed all twelve with title, kind, age and five acts offered, and opening
  one rendered "The record could not be read. id Invalid string: must match pattern …". The
  identifier is spelled once, in `RecordIdSchema`, and every reading door takes it — but
  `records.id` carries no CHECK and the crossing validated the table name and the column names
  against the migration and then inserted whatever values it was handed.

  What makes it a door-time defect rather than a display one is that **there is no repair**:
  `records_kept` refuses DELETE below the doors, `INSERT OR IGNORE` cannot rewrite a row, and no
  door deletes a record. An unopenable record is permanent for the life of the store, so the only
  place the guard can be is the way in. The chunk is now refused whole rather than row by row,
  because a half-delivered import of an append-only table cannot be taken back either.

  The guarded columns are **derived from the migration** — a column that says
  `REFERENCES records(id)`, or whose name is one the frontier only ever writes a record id into —
  so a column added later is guarded without anyone remembering to guard it, and a test pins the
  derived set. That is the argument `machineColumns()` already made in the same file about the
  same crossing: a guard over two of three columns reads as a statement that the third is fine,
  which is how `runs.machine_id` went untested with a derivation bug behind it (#378, #379).
  `edges` is checked against the row's own `from_kind`/`to_kind`, since those ends legitimately
  hold entity identifiers too, and a name-based guard would refuse every entity edge.

  No real row becomes unimportable: the Go tree minted a family and sixteen random bytes in hex
  (`internal/frontier/store.go` at `v0.4.0`), which the pattern admits. One test fixture did not,
  and it was the fixture that was wrong — an invented id shape is how the hole stayed open.

- **The judgement part could not call a single Babel door.** It declared `atyrode.babel` a
  required dependency and then held no capability to use it: a cross-plugin call is bounded by
  the **caller's** own ceiling, every Babel read door carries `containers:read`, and the part's
  manifest carried `services:invoke` alone. The declared edge opened nothing. It declares
  `containers:read` now — and deliberately **not** `containers:write`, which is the authority
  every ruling door carries and which #360 holds open; a pin makes widening it a failing test
  rather than a quiet edit.

  Nothing could have caught it. Every fake in the repository hands a handler a synthetic context
  and calls it directly, which is one layer *below* where the ceiling is graded — the refusal
  happens before the callee is asked, so a fake that starts at the handler can never see it. The
  test added for this walks the host's rungs in the host's order and raises the host's own
  sentence, with the grading line taken from the server and the wildcard rules imported from the
  protocol rather than restated. It models the governed-capability subtraction, which is the part
  that matters here: `services:invoke` is governed, so the part's usable ceiling is the read
  alone.
- **The `controversial` order ranked agreement above disagreement.** A record the voters split on
  is supposed to surface rather than land mid-ranked, and most of that shipped with the two axes:
  the split is computed, the row is badged, the gutter is marked, the peel says so and the
  standing filters on it. **The order itself was the one piece missing, and it was inverted** — a
  record every role agreed on, cross-role, outranked one genuinely divided inside a single role.
  Rank is balance times magnitude *within each role that took both sides*, summed over the roles
  that did, and zero when no single role divided; a zero-ranked record now drops out of the list
  the way silence already drops out of `rising`, so the list is exactly the split records and its
  total is how many of the window are contested.

  The empty state says **both** readings of its own absence rather than claiming the corpus
  agrees. #366 flagged the doubt itself: if the roles are redundant — several reviewers asked one
  question several ways — then a cross-role mixture is a real disagreement and this order
  under-counts by exactly those records. Nothing measures which, so nothing pretends to.
- **A release delivers every bundle it packed, and nothing says which by hand.** The release job
  handed the preview's receiver a hand-maintained list of plugin ids while everything around it
  globbed, so a new part was attached to the release and never delivered — and the guard could not
  notice, because it checked that every id it *asked* about had a checksum, which says nothing
  about a bundle nobody asked about. A tag cut the day the judgement part landed would have
  attached four bundles and delivered three. The order is derived from the packed bundles' own
  declared dependencies now, through the kit's own `familyOrder` — the same function `verify` and
  `dev` install by, rather than a second topological sort — and the guard is reversed: **a bundle
  in `dist` that nobody delivers fails the job.**

  A prerequisite that is not in the set keeps its place and is named on stderr rather than sinking
  to the end or being dropped. Only the hub knows whether an external dependency is already
  installed, and ordering an unresolved bundle last would put a baseline after its own parts — the
  exact refusal the ordering exists to avoid.

  Two more defects in the same loop. The published checksum was compared against what the script
  was told rather than against the bytes it read, so it proved the asked-for were packed rather
  than that the delivered were right. And **`ssh` without `-n` swallows the rest of the list**:
  with a faithful stub, the loop delivered one bundle of six and exited 0. Both are fixed, and the
  second is the kind of green that is worse than a failure.
- **The crossing guards every machine column, and reads the list out of the migration.** The guard
  that refuses a chunk hosting rows on a value the hub cannot describe covered `sessions.host` and
  `runs.machine_id` by a hand-written ternary, and the schema had a third — `drains.machine_id`.
  A guard covering two of three invites the reading that the third is fine, which is exactly how
  the `runs` arm came to have no test at all and, behind it, a column-derivation bug that made
  that arm unreachable. The columns are now derived from `SCHEMA_V1` the way the crossing's own
  importable-table list already is, so a new machine column is guarded **with no edit**; a test
  pins the derived map, so a change to the set fails in either direction — a new machine column
  that nobody expected, and a non-machine column that merely takes the shape.

  Deriving it found a fourth nobody had named: `run_calls.transcript_host`, added hours earlier
  with the per-call trace, which is the locator of a run's transcript and would have taken a host
  name as happily as the other three.
- **One stop reason named both a blocked loop and a healthy one.** Reading all nineteen reason
  words as a set for the first time — which moving them into the contract forced — turned up that
  `batch` meant two opposite states of health: three sites where every review slot was held by
  somebody else, and one where the loop filled the batch itself. Watch treated it as the healthy
  stop and stayed silent for all four, so **a batch wedged by stale claims read exactly like a
  loop working at capacity**, which is the failure the cycle panel exists to end. The success
  case is `batch-filled` now and stays silent; `batch` says "every review slot is already claimed
  and none of those reviews has finished."

  Two gap reasons named different subjects under words that read like synonyms: `retired` is about
  the topic's lifecycle and never refers to a record, and `replaced` is about the record's own and
  silently contained the retired ones. They are `topic-retired` and `record-replaced`, renamed at
  the source rather than annotated in the panel, because the words travel further than the panel
  does — a door's enum, a tally key, a log line, whatever filters on them next. `record-replaced`'s
  label was wrong as well as ambiguous: it said "superseded by a newer revision", which is false
  for the retired half.

  The coordinator's own `disabled` stop stays, with a comment saying what it defends: `draw()`
  re-reads the policy at its own moment while the conductor read it at the top of the tick, so a
  policy change landing in that window would otherwise hand out an assignment and reserve a claim
  on a deployment nothing authorises. Dead-code removal is right when a branch cannot change
  behaviour; this one can, and it costs a boolean.

  Exhaustiveness was proven rather than asserted: adding a twentieth reason makes `tsc` fail on
  the label table.
- **A posting's refusal carries the hub's own word beside the operator's sentence.** The drain
  controller recovered a job the hub already held by matching refusal **text** —
  `started.refused.includes("job_digest_conflict")` — because nothing else was available: the hub
  throws a bare word, and the launch wraps it into a sentence for the operator, so no code
  survived to the caller. Every refusal a launch can hear arrived as one prose field, and a caller
  that must behave differently for one of them had to read prose. `Started`'s refusal now carries
  an optional `code`, present exactly when the hub refused and absent when the sentence is
  Babel's own, so its absence means one thing. The drain branches on the word; changing the hub's
  wording does not change the behaviour, which a test proves with a fleet that refuses with the
  code in a message that does not contain it.

  The code is read from a **field of the error the hub threw**, never from a sentence anybody
  composed — the two composed sentences on that path are excluded on purpose, including the kit's
  own message, which glues a prefix in front of the host's word, so matching on it would already
  be matching a composition. The typed rung is first in the ladder and is the only thing that
  changes when the SDK grows a typed job refusal. This is the shape `machine/results.ts` already
  uses for submissions, where the drain has tallied refusals by code rather than by wording since
  it shipped.
- **The run log can cross at all.** `importLedger` refused every `runs` chunk with
  `runs has no column "payload"` — the column every receipt carries. The crossing derives each
  table's columns by reading its `CREATE TABLE` body, and the derivation treated the apostrophe in
  `runs`'s own inline note — "where this run's job is" — as the start of a string literal. Nothing
  closed it, so the rest of that table was read as one quoted run and its last two columns,
  `unreadable` and `payload`, never became columns. `runs` was the only table of twenty-six
  affected, which is why it survived: every other table's comments happen to be apostrophe-free.
  A comment is now skipped rather than scanned, because a comment is prose and prose carries
  apostrophes, and a literal rides into the part whole so a comma inside it cannot end a column.
  The full `runs` column list is pinned, so the next comment with an apostrophe in it fails a test
  rather than silently truncating a table.

  It also means the machine-identity guard was only half reachable: the `runs.machine_id` arm
  could never fire through the door, because no `runs` chunk got that far. That arm had no test
  either — removing it left every suite green — and now it has one.
- **Three tool usage lines named a path that does not work.** `import.ts`, `seed-recipes.ts` and
  the new sweep printed `bun tools/…`, which has been wrong since the layout flattening moved the
  tree under `atyrode.babel/`. They print the invocation that runs.
- **Concurrent draws no longer converge on one assignment.** Observed live with twenty-three
  review workers: most draws returned `conflicting claim: assignment eval-a-… is held by another
  worker`. The reserved lanes pick the oldest due, which is one deterministic head, and with the
  default `coverage_share` half of every cycle reached for it — so all but one worker lost, and
  the loser paid the whole candidate build again, seven paged scans over the frontier, before
  failing. Ranking still fixes the order a draw walks; it no longer names the only candidate the
  draw will consider. A head another worker holds is stepped over, and "holds" means both a live
  claim read fresh from the ledger and an assignment this process handed out in the last thirty
  seconds and nobody has claimed yet — because at the instant several workers draw there is
  nothing in the claims table to skip, the claim lands after the draw returns. A draw still
  writes nothing and charges nothing. Re-selection is bounded at three rounds, each costing one
  indexed point query rather than a rebuild; past the bound the top pick is handed out unchanged
  and the claim refuses it exactly as before, because a conflict the caller already handles beats
  a draw that will not terminate.
- **Watch lists every recipe in force, not only the ones that have run.** `policy().recipes` was
  a `GROUP BY` over the runs table, so a recipe the operator installed and nothing had ever
  performed was absent from the panel altogether — seventeen in force, two on the screen, and
  nothing anywhere saying the other fifteen existed. The policy's own declared list is the axis
  now and the runs are joined onto it, so zero is a number rather than an omission; a never-run
  recipe carries the same badge that marks one switched off, drops the `ran … · N runs` line
  instead of printing a zero among real counts, and the section lede tallies how many of the
  cookbook has never been opened. The declared list is read in exactly one place, which the
  coverage grid already needed and had to build for itself. A recipe the corpus ran and the
  current document no longer declares still appears, trailing the declared ones: those runs were
  paid for and dropping them would hide them. The Start form's picker inherits the fix — every
  declared recipe is now launchable, where before only one that had already run could be chosen.
- **A refused review keeps what it submitted, so its refusal can be measured rather than
  believed.** A review the contract refused left a reason string and no evidence, so neither "did
  that class of refusal fall after the contract changed?" nor "did the judgement change between
  what was refused and what was recorded?" could be answered from the store at all — and a shape
  rule loosened on an unmeasured claim is the wrong rule removed for the wrong reason. The run's
  receipt now carries `rejectedSubmission`: the answer as the model wrote it, unedited, because an
  edited one is not evidence. A submission too large to keep is reported as its size instead of
  truncated into something nobody submitted, and a session that answered nothing carries no
  payload because there is none (#311).

- **A crossing chunk naming a machine the hub cannot describe is refused, and the corpus already
  written can be re-hosted.** The importer fix stopped new rows carrying the Go deployment's host
  name, but it could not stop an operator passing a name to `--host`, and it could not repair the
  588 rows already catalogued. `importLedger` is the one place a chunk enters with hub authority,
  so it now refuses a chunk whose `sessions.host` or `runs.machine_id` names something `describe`
  refuses — which also covers `runs.machine_id`, the other half of #309's note, with the same
  guard instead of a second one. `atyrode.babel.rehostSessions` moves every row under one host
  value onto a machine id the hub has just described: owner-only like the crossing it repairs, so
  the repair cannot install a second unreachable value, and idempotent so a second run reports
  zero. Nothing infers which machine a name meant, because nothing can — the hub resolves no
  names and no door a plugin is served lists machines, so the operator states the mapping and the
  hub verifies the destination (#312).

- **A review's contract offers a skip or an assessment, never both.** Declining is not opposing
  (`cookbook/recipes/babel-triages-the-queue.md`), and `acceptReviewResult` has always refused a
  submission that did both — but the schema printed into the review prompt offered `skip` beside
  `vote`, so five of one day's sixty-six runs were discarded whole for composing a shape the
  contract they were shown permitted while a paragraph elsewhere forbade it. The generated schema
  is now the union of the two answers a role may give, so the contract cannot spell the mistake.
  It is documentation strength rather than a guarantee — the schema is printed, not registered as
  a provider-constrained output, so a model can still type both, and `acceptReviewResult` is
  therefore not a belt but the only enforcement there is, which its comment now says. A model
  echoing `skip: ""` beside its assessment is still submitting a valid assessment. Also fixed
  while in the file: the refusal that read "is a objection" now agrees with its own article
  (#311).

- **The crossing records the machine id it was given, not the Go deployment's host name.**
  `sessions.host` is a hub machine id — `describe`, `listRuns` and `machines.repository` are all
  keyed on it and the hub resolves no names — and `tools/import.ts` documents `--host` as exactly
  that, warning in the same paragraph that "a corpus imported under `dev-01` is a corpus every
  readiness check answers about a machine that does not exist". It then wrote the `host` recorded
  in the Go `run_preparation` selection a session appears in, which is that name. Every
  catalogued session appears in some preparation, so the documented id was replaced for all of
  them and 588 rows arrived that nothing could reach. The Go name is no longer read for this
  column. Proved by a fixture whose Go selection host and `--host` now differ — the old one used
  one value for both, which is why nothing caught it — and the two import tests fail against the
  previous code (#312).

- **The conductor's cadence is registered on a machine ID, and a host it cannot use says so.**
  The loop picked the machine for its beat out of `SELECT DISTINCT host FROM sessions` unioned
  with `runs.machine_id`, and handed those strings to `jobs.describe`. Both columns are written
  from `tools/import.ts --host`, which an imported corpus gives the operator's own host name —
  588 rows of `dev-01` — while the hub keys `describe` on the machine id and resolves no names.
  It answered `connected: false` for the name, nothing was ever usable, and the schedule was
  reported `absent` with no note at all: a healthy-looking cycle beside an empty
  `job_schedules`, which is why a whole feature was missing for a day without anything saying
  so. The beat now registers on the machine the POLICY names (`review.machineId`) — the one
  machine id an operator recorded, and already the machine the same cycle dispatches every
  review to — plus, for folding beats it did not launch, the machines the hub's own
  `jobs.schedules()` rows carry. Every refusal names the machine and the cause, in the shape
  the hub's own `job_owner_mismatch` and `installation_absent` arrive in, and the door's
  private copy of those sentences is now one `describeHost`. Folder identification asks only
  about ids too, and names the hosts it left alone instead of buying a refusal per folder per
  cycle about a machine that does not exist. There is no migration, because Babel's store holds
  nothing to backfill an id from: `--host` is documented as the HUB machine id instead, which
  is where the name should have become one (#309).

- **A contribution the review contract refuses no longer discards the review around it.** Seven
  conductor-drawn reviews on a free non-reasoning model spent 202k tokens and recorded two: five
  were refused whole because one contribution broke a rule about itself — an objection that named
  an alternative, a contribution carrying neither text nor evidence, a citation the record never
  served — and the vote and the good contributions beside it were paid for and thrown away
  (#305). Handing the validator's sentence back to the same session is not available to this
  plugin: Code publishes `runSession`, `readSession` and `cancelSession`, whose input is a prompt
  and whose answer is a sealed transcript, and omp's `resumeSession` prepares an interactive
  terminal rather than a governed one-shot, so a session that has sealed cannot be spoken to
  again. So the rules whose whole subject is ONE contribution — stated once, in
  `contributionRefusal` — now refuse that contribution by name, and the rest of the review goes
  through the same `acceptReviewResult` it always did, as a whole, and is recorded only if it
  stands without what was dropped. No rule is relaxed and nothing is repaired: a refused
  contribution is dropped, never edited into one that would pass; the vote, the outcome, the
  skip, the filing and backlog answers, the refinement-depth bound and the scope rule are
  statements about the review as a whole and still fail it whole; and a claim whose only support
  was refused falls with it, so an observed outcome cannot survive losing the evidence for it.
  The receipt now carries `refusedContributions` — `<code>: <sentence>` apiece, the shape a
  `reason` already has — with `counts.contributionsRefused` beside it, and the conductor counts
  each refusal into the cycle's tally, so "the model would not follow the contract" is countable
  instead of being invisible behind a discarded run.

- **The doors that ask a machine what it can run are lent that read, so the loop keeps its own
  cadence.** `engine.jobs.describe` moved onto `machines:read` (atyrode/manifold#736) and the
  bridge a dispatch is served is the door's own caps plus its delegates — so every describe
  behind a Babel door was refused `job_capability_absent:machines:read` however privileged the
  operator's key was: a press was refused before the machine was asked, and the conductor could
  not find a host to register the beat on, which left Babel beating only while somebody kept
  pressing something. The read could not be delegated at all until atyrode/manifold#740 added it
  to the closed delegable set; `pulse`, `runs`, `launch`, `drain.start` and `drain.status` — the
  five that describe, directly or through the cycle behind them — now name it, and nothing else
  does. The caller is unchanged and still needs only `containers:read`; a delegate is the door's
  ceiling, intersected with the caller's own capabilities and the plugin's install grant, and the
  engine still requires version-bound consent at each operation node. The plugin tests drive a
  `pulse` through a bridge attenuated exactly as the host attenuates one and assert the cadence is
  registered from it.

- **The reading surface's clock stopped when the hub did, and one confirmation froze the feed
  for everyone reading it.** The `react-hooks` rules installed with #327 found four render
  defects and were held at `warn` until the rendered surface could be exercised; all four are
  fixed and both rules are back at the plugin's own `error` (#345). Home and the record panel
  re-based their clock inside the effect that received an answer, so a quiet deployment printed
  "just now" on a record hours old — ages now follow the wall clock's own minute, which is the
  smallest word `since` has, whatever the hub is doing. Home also declared its hold on the
  shared feed by writing a ref during render, and the predicate it wrote — "a row has recorded
  something" — was emptied only by a new question, so the first ruling of a session held every
  reader's copy of that feed for the rest of it; the claim is now written inline, where the kit
  commits it after a render actually survives, and it is bounded by the act's own stamp. The
  topic panel mirrored its subject into state behind an effect, which is why a topic the reader
  had never expanded opened at the depth he had paged the last one to; the narrowing is derived
  now, and a new subject opens at the top of its own list. Four panel tests hold them: two watch
  an age cross a minute while the hub answers the same thing, one answers a question and then
  watches the list let the world back in, one pages a topic and changes subject.

- **An empty account selection is caught before preparation, not afterwards.** Babel asks Code
  about the selected profile revision after checking local eligibility and before sealing either
  exploration or automatic-titling material, and every session posting uses the same guard.
  Refused titling leaves the batch eligible for a later cycle. A positively resolved empty
  selection returns `engine_no_account`; a moved revision is stale, not account absence.
  An unresolved observation still leaves the decision to Code, which revalidates when posting.
  No provider credential or new capability enters Babel. The regressions prove that refusal
  posts neither preparation nor session, that local rejection needs no Code call, and that
  Code's later refusal remains authoritative. A read-only smoke against the preview's actual
  Code door also exercised the pinned profile schema and stale-revision rejection (#255).

- **A preparation no longer re-reads a corpus it has already read.** Reading logs is the whole
  cost of `prepare`, and the answer never depended on which run asked: on 2026-09-12 twenty
  concurrent explorations over overlapping scopes read and hashed the same sessions twenty times
  — ~12 GB per draw, load 41 on twelve cores with not one model call in flight, and an OOM that
  took the operator's own editor with it. A machine now keeps its reading of each settled log —
  both digests, the record count, the preflight's report and the normalized, redacted record
  stream itself — in the managed `atyrode.babel.cache` location the job already declares,
  written in the same single pass that seals the material. An entry is a claim about an
  OBSERVATION and not about the corpus: the path, the size, the mtime, the normalization
  schema, the detector set and the preflight mode are all re-observed before it is used, so a
  log that moved is not invalidated, it simply fails to match, and #337's refusal of a stored
  position applies unchanged — there is no second authority here to disagree with the
  filesystem. One entry per session, so the cache is bounded by the corpus rather than by how
  often it is prepared. What makes it admissible at all is the exclusion beside it: a log whose
  bytes could still be moving is never in a scope, so size and mtime are only ever asked about a
  file that settled minutes ago. And the stream is verified rather than trusted — it is
  re-hashed as it is replayed into the material, and bytes that do not digest to what they were
  kept as refuse the scope and drop the entry, so the source digest a citation carries is always
  a digest of what was actually sealed. The suites prove the observable rather than a duration: a
  second preparation over an unchanged scope opens **no** session log, produces the same
  preparation id, the same preflight report and byte-identical material; a log that moved is the
  only one read again; a stream kept unscanned is never served to a preparation that redacts;
  and a corrupted entry costs one refusal and not a machine that can no longer prepare.

- **The specification described work Babel no longer does, and did not describe work it does.**
  Four behaviours had landed without `SPEC.md` saying so — the drain (its target, its allocation,
  its controller, its four endings and the one record every ending leaves), the claim that dies
  with its job, what a run reports while it is still running, and the fact that a review reads its
  assignment and the sealed material rather than the corpus — so a conformant implementation could
  still have digested the corpus once per review, which is the design that cost a whole drain day.
  Two statements had also gone false: §4.10 said nothing indexes the corpus, and `docs/parity.md`'s
  `index/` row named a closed issue, while an index over Babel's own **records** now exists behind
  the `search` door. Both now say which half exists and which does not — the session corpus is
  still unindexed, which is #415 — because "retrieval is built" would be wrong by the larger half.
  `SPEC.md` §7.1 is new; §4.10, §4.12, §5.7, §6.3, §6.4, §7 and §9 gained a paragraph each, and
  §6.4 no longer says nothing recognizes an input it has already prepared, because a machine now
  keeps its reading of a settled log.

- **A citation's quoted text is now checked against the line it cites, and a claim still cannot
  cite outside the corpus it was served.** Until now a locator was checked only against the
  material's index — the path was served, the digest matches — which says nothing about whether
  the quoted span is actually there, so a fabricated quote and a real one were the same row. Of
  300 digest-verified citations in the imported corpus, 86 carried a quote of twelve characters
  or more; 53 matched the cited line, 57 matched somewhere else in the right file and 29 matched
  nowhere in it, and a model asked to judge the same 86 scored 60.5% against a 62% majority
  baseline. It is string matching, so code does it: a locator now carries `quote`, the
  settlement reads the cited member of the sealed material back and writes what it found —
  `verified`, `moved`, `absent`, `unquoted` or `unchecked` — onto the record's own evidence,
  where the peel renders it beside the words and the receipt counts all five so a deployment can
  measure its own rate rather than argue from someone else's.

  **The two halves refuse differently, deliberately.** A path outside the selection still refuses
  the whole answer as `unknown-reference`: it is a claim about bytes nobody served and nothing
  later can check it. A quote that is not where it says it is MARKS and refuses nothing — it is a
  claim about real bytes that is wrong about where they are, and discarding a paid run over one
  is the all-or-nothing waste #231 and #311 measured. The admission is a whitelist of the exact
  spellings the index carries rather than a resolver, so `sessions/../sessions/<file>` is refused
  even though it would resolve back inside the material, and bytes are only ever reached through
  the index entry and never through the string the model wrote. The normalisation is written into
  `babel/server/engine/citations.ts`'s header and biased one way: whitespace, line endings,
  Unicode composition and the JSON escaping of a canonical record are not content, so a
  re-wrapped quote is not an accusation, while case and punctuation are content and a span under
  twelve characters is `unchecked` rather than verified. The cost an owner should read twice is
  in `docs/sandbox-threat-model.md` residual 9: checking a quote means the hub reads the cited
  member of the sealed material into its own memory, once per quoting answer.

- **A claim died with its worker and nobody said so, and a refusal nobody paid for read as
  money spent.** Two halves of the same blindness. A claim whose job was over kept its batch
  slot until its lease ran out — 86 minutes each on 2026-09-13, seventy of them over the
  top-ranked subjects — because a slot was counted from the claims table alone. The store now
  counts a slot only while a job is still running behind it, so a closed run frees the slot on
  the next read rather than on the next settlement, and the conductor's reaper is BOUNDED: at
  most 128 dead claims a cycle, oldest grant first, with a note saying what it left, because a
  reap holds the write lock a dispatch is waiting behind. The three orphan kinds are now the
  query's own `WHERE` clause, so the bound bounds the reap and not the reading.

  The other half is what a refusal cost. The park heuristic cleared itself for any refusal at
  all, so three answers the deployment bought and the contract threw away looked exactly like
  three it never paid for, and the loop kept buying. A refusal is now filed by whether a model
  answered — the hub's own meter where one is attached, the sealed submission where none is —
  and the two are never added together: the pulse counts `refusals.paid` apart from
  `refusals.free`, and the park carries `spent` and `barren` as separate counts under a reason
  word from `babel/contract.ts`, with the sentence naming the remedy each implies (the recipe,
  or the machine). A streak of paid refusals now parks too, which #265 did not ask for: a lane
  that buys three answers nobody can use is worth stopping for, and one answered review, an
  hour of quiet or a new policy version lifts it exactly as it lifts a barren park. The day's
  tally is parsed back through those enums on every wake, so a word no build has spelled
  cannot reach a reader at all. Proved by a bounded reap of 130 ghosts across two cycles, a
  freed slot drawn into by the same cycle that freed it, and one metered call moving the same
  refusal — same code, same zero cost — from `free` to `paid` and the park from `barren` to
  `spent`.

- **The model a receipt named was the one that answered last, filed under the field that means
  "what it asked for".** A run reaches a model through a Code profile and the transcript names
  whatever actually served the turn, so a fallback, a retry or a steer moved it silently and
  `receipt.model` — documented as the ASK, read as the ask by `RunTrace` and diffed on the
  request side — was quietly a statement of fact wearing a statement of intent. Two runs of one
  identical request, one of them served by a substitute, therefore compared as
  `different-request`, a verdict that disqualifies every other field of the comparison and
  destroys exactly the same-request-twice measurement the trace exists for. `model` is now the
  launch's own `askedModel` and nothing else — absent for a conductor-dispatched review, which
  names a profile and never a model, because absent is the truth about it — `models` is what
  answered, and a new answer-side diff field `answered` names the models call by call.

  **And on the metered lane nothing recorded what answered at all.** The fold saw a model on
  every `inference_call` and kept only the newest; `usage.inference` is five numbers and no
  name; the row holding the newest was deleted by the settlement. So `run_progress` now keeps
  the distinct models in first-heard order, bounded at eight, the settlement reads them one last
  time before dropping the row and keeps them in the receipt, and one `models` field on a run
  answers for both halves of its life — the fold's list while it runs, the receipt's after.
  Watch shows `sonnet (+1 earlier)` on a live row and the whole chain on a settled one.

  **A progress row also stopped being read as the present.** Nothing deletes one but a
  settlement, so a job whose hub went quiet — or whose loop stopped waking — left its last fold
  standing and the panel rendered `at the model since T` over a clock still ticking for a job
  that died an hour before, which is the 2026-09-13 header in miniature. A fold older than five
  minutes is now `unheard`, judged at read time against the reader's own clock: the stage is
  still shown, because it is the last true thing anybody observed, but as `last heard 10m ago`,
  and the header stops counting it "at the model". The word is deliberately not `stale`, which
  §4.13 gives to a record and defines as the one judgement no clock makes; unheard is a
  statement about the report and not about the run, and a job may be perfectly alive and
  unheard. Proven through the `runs` door a panel calls — a live row's stage, spend, models and
  freshness, a settled run's models off its receipt, and a fold nobody refreshed coming back
  unheard — and in the panel document.

- **One unusable item no longer discards the whole paid run.** An exploration is one agent session
  over a large corpus, so by the time anything validates the answer the tokens are gone — and
  persistence was all-or-nothing, which converted a partly-wrong result into zero value at full
  price. On 2026-09-12 several runs finished their model work and lost all of it at persistence:
  one disposition naming a workspace the machine did not have, one objection attacking an id
  nobody held, one observation that forgot its counter-evidence position. The conductor then
  parked reporting that the cycles had spent nothing (#231, post-mortem F16). A submission is now
  partial: the items that clear the contract are recorded, the items that do not are refused by
  name with their reason on the receipt's `refusedItems` — a JSON Pointer into the document the
  model submitted, so the item is findable in `rejectedSubmission` beside it — with
  `counts.itemsRefused` beside them and the cycle's report naming each dropped item and its
  reason. The cycle's `refusals` tally is untouched by a run that stood: it answers which
  submissions the deployment paid for and got nothing from, and a run that recorded nine items
  and dropped one is not one of those.

  **What "kept" means for a result read as a set is the largest subset closed under §4.2's
  development path**, and the floor is the only judgement in it. A refused item takes with it
  everything whose support ran through it — a candidate's observations and its remedy, a finding
  resting on an observation that fell — and nothing else, which answers the objection the code
  used to make ("half a development path is worse than none"): the half that is worse than none is
  the half that dangles, and a path-closed subset never does. Below
  `SUBMISSION_KEPT_FLOOR` — half, because that is where "mostly worked, one item was wrong" flips
  to "this answer was not written against this contract" — the submission is refused whole, and it
  is still spend: the refusal, every item of it, and the cost reach the receipt and the claim is
  finished either way.

  **The same shape is no longer judged in two places.** `machine/results.ts` validated the
  submission and `server/engine/records.ts` re-judged it on the way to the rows, in its own words
  and sometimes under a different code — a consolidation resting on a proposal was
  `development-path` in one and `unknown-reference: no observation f1` in the other, for one
  submission, depending on which saw it. The second copy is deleted, `exploreRows` can no longer
  refuse anything, and the locator check is now asked PER ITEM of the one module that states it
  (`server/engine/citations.ts`, `unservedCitation`) instead of once over the whole result —
  which is what finally makes the prompt's own promise true: the claim that cited bytes
  it was never served is refused and its siblings are not. Proved by a run whose retyped digest
  costs its observation and the finding on it while the candidate and its question are recorded,
  by a wholly unusable answer that writes nothing and still settles its claim at cost, and by the
  evaluation half's own scope rule refused under the same code from the engine and from the store
  (#263, #311).

## [0.4.0] - 2026-09-14

### Removed

- **Babel launches nothing: the self-launch engine, the Start picker and Babel's own inference
  service are gone.** v0.3.0 had `explore` and `evaluate` launch `omp --mode rpc` from a pinned
  runtime tool and meter it through an `atyrode.babel.inference` policy a `setupInference` door
  installed, with a Start form that chose the account, the model and how hard it thinks. That
  was the wrong architecture: `atyrode.babel` depends on `atyrode.code`, which depends on
  `atyrode.omp`, and Code owns the profiles and launches omp. When Babel's button is pressed,
  Babel either names a saved Code profile or opens Code's generator so the run is parametrized
  there, then posts the run through Code's `runSession` door — it never composes a session and
  never launches an engine. So the machine half's `explore` and `evaluate` operations, the `omp`
  pin, the RPC driver, the inference service, the `setupInference` and `accounts` doors and the
  Start picker are removed; `scan`, `prepare` and `archive`, the drain, the receipts fold, the
  release path and every reading panel stay (#279, #268).

### Added

- **A release delivers Babel's dependency closure to the preview.** Babel requires Code and Code
  requires omp, so a preview that received Babel alone refused composition. A `v*` tag now builds
  omp's and Code's bundles at the pinned revisions through the same `deps:code` the gate verifies
  against, attaches them to the release beside Babel's three, and the receiver installs them in
  dependency order - omp, then Code, then Babel, baseline before parts. The plugin gate the
  release runs is the same pin the PR gate uses (`476a586c`) with `deps:code` in front, as
  `manifold-plugins.yml` already had. Proof is the release run's deliver job: ten `plugin` calls,
  each answered by the receiver.
- **Babel runs are Code sessions: a Code profile, `code.runSession` through `ctx.actions.call`,
  and the material as a job input.** The Start section is a form again — three requests, a
  machine and a list of the CODE PROFILES Code answered, each naming the model it will run as
  and where Code last posted for it, with a link to Code's generator for the workspace chosen
  and no model, thinking or account field of Babel's own. Pressing it selects the sessions,
  posts Babel's own `atyrode.babel.prepare` job — which now seals a SECOND output, the
  material: `index.json` plus one canonical record stream per session, written in the single
  pass the digests were already taken in — composes the prompt around `/inputs/material`, and
  asks `atyrode.code.runSession` to post the session. A settled session is reconciled through
  `code.readSession` rather than `ctx.jobs`, because Code's job belongs to `atyrode.omp` and a
  settlement of it never reaches Babel: its final message is read for the answer, every
  citation is checked against the material's index, and the receipt is written with the model
  and what it spent — a refused submission included, at its cost, because the model answered
  and the account is CODE's own report. The prompt is bounded in BYTES against
  `PROMPT_MAX_BYTES` — the hub's 64 KiB job-input map is the real ceiling, so the check is a
  `TextEncoder` and not a character count — and a run over it closes `prompt_too_large` with
  both figures rather than a Zod issue from Code's parse (#279, #268, #258, #264).
- **The material is a bound job input, and a run is started in two wakes.** Manifold's
  job-inputs primitive (ADR 0044, atyrode/manifold#592) binds one job's sealed output into
  another's sandbox, so `atyrode.babel.prepare` now declares `exports: ["material"]` and a
  posted session carries `inputs: [{name: "material", from: {jobId: <prepare>, output:
  "material"}}]`. A binding names a job that has SETTLED, so the press seals the material and
  records the run's intent and the session is posted on the wake that preparation's own
  settlement causes — which is also why the prompt is composed from the material's real index,
  with the file names and digests a citation must copy, instead of the layout the press could
  only guess. **A stop between the two wakes** cancels the preparation and CLOSES the run,
  because the posting wake walks every open row whose material sealed: a row left open would
  post its session after the operator pressed stop. What stops a run is read off the row and
  not off the drain's own bookkeeping — the container says which lane, and the job id in it
  is the one that lane minted (#279, #268).
- **A drain names a Code profile, a session is posted when its material is sealed, and the
  dependency on Code is required.** The drain's five typed fields — provider, credential id,
  identity key, model, thinking — are gone: a drain picks a Code profile from the same list
  Start does, and what it records about the model and the account is CODE's own report,
  copied once at the press and labelled as that (`ctr_x: victorballu (as Code reported at
  start)`); Code saying it could not resolve an account is said as that and never as "spends
  nothing". A press now seals the material and records the run's intent, and the session is
  posted on the wake that preparation's own settlement causes, because a job-inputs binding
  names a settled job's output; the prompt is therefore composed from the material's real
  index rather than from guessed file names. A run's session is read, and cancelled, through
  Code — `readSession` answers a live job with a null receipt instead of a refusal, and an
  operator's Stop closes the run `stopped` rather than `failed` — and `atyrode.code` is a
  required dependency, so `bun run deps:code` builds Code's bundles and omp's and `bun run
  verify` composes all three families in order (#279, #268, #267).

## [0.3.0] - 2026-09-14

### Added

- **A drain is a governed, measured, self-stopping operation.** `drain.start` names the account
  and the model it spends, the fan of jobs to keep in flight and where to stop — a metered cost,
  a number of output tokens, a deadline, or it is refused; a drain that names no deadline is
  given one, two hours out. `drain.status` answers jobs live, jobs at the model, tokens and cost
  a minute over the last three minutes, the spend against the target and the ETA against the
  deadline, refusals by reason and the account being burned; and `drain.stop` cancels what is in
  flight and says how many it cancelled and which it could not. The controller keeps the fan
  filled on every settlement through the same launch path the operator's own button uses, stops
  itself at the target or the deadline, and governs nothing: its jobs are launched directly and
  take no claim, so no admission bound counts one and there is no overlay to set — the fan is
  bounded against the manifest's `concurrentJobs` at the door and by the drain's own live jobs in
  the controller, and what one run may spend stays the standing policy's. What it cannot cancel
  it keeps: a drain holding jobs is `closing`, folding their receipts until the last one lands,
  because their spend is its spend. Watch grows a Drain section with those figures and a Stop
  button. Two hours and fourteen minutes of the 2026-09-13 drain produced fifty reviews, no burn
  rate, no self-stop that ever fired and no way to say which account was being spent (#258, #267).

- **Babel launches its own engine and meters it through its own inference service.** Code's
  `code engine` is gone (atyrode/code#153) and Manifold has no plugin-to-plugin call, so
  `explore` and `evaluate` now spawn `omp --mode rpc` themselves from a manifest-pinned `omp`
  runtime tool and reach a model only through the `atyrode.babel.inference` binding, whose
  runtime is omp's own gateway — so the job holds no credential and the machine owner meters
  every call (ADR 0038). A launch names its model, its thinking level and the account it spends;
  the receipt records all three, the per-run ceiling rides on the request as
  `limits.inference.costMicros`, and a failed launch carries a named cause
  (`broker_unavailable`, `rate_limited`, `inference_unbound`) instead of a sentence about a
  missing ready frame (#279, #277, #267, and the hub half of #256).
- **Babel becomes a manifold plugin family.** `plugins/atyrode.babel` is the
  baseline — one SQLite store of its own (manifold ADR 0034), the doors over it
  and the five event kinds it originates — with `atyrode.babel.feed` (Home, the
  peeled record, a topic) and `atyrode.babel.watch` (what runs) as its panels,
  in-realm React on `@manifold/ui`. The vocabulary is spelled once in
  `contract.ts` and the twenty-three append-only tables once in
  `store/schema.ts`; the shape is created whole by the plugin's own enable hook
  and a purge is the file. `bun run check`, `bun test`, `bun run pack` and
  `bun run verify` gate it against a real engine spawned from the pinned
  manifold checkout, in CI through manifold's reusable `plugins.yml`. Decision
  91, `docs/manifold-plan.md` P0, #241.
- **The plugin does what the Go product did, minus the machine.** The crossing:
  `tools/import.ts` carries every record, edge, status event, disposition,
  filing, assessment, claim, policy, entity, alias, fact, question, plan, run
  and session out of `durable.db` and the catalog into the plugin's store with
  ids kept and one `imports` row per table, idempotently (90,718 rows from the
  operator's own store, twice, identical). The read model: the feed index, the
  six sorts with their tie-breaks, needs=me, the five-depth peel, the thread,
  topics from live filings, pulse, runs and the policy - nine reading doors.
  The acts: rule, comment, answer, interest, file, unfile, tell and setPolicy,
  every one an append; a ruling on a topic or backlog proposal applies its plan
  or declines it with the note as the reason. The coordinator: lanes, the
  weighted draw, claims with fences and takeover, the day's spend against the
  ceilings, the measured lease floor. The machine half: `scan`, `archive`,
  `prepare`, `explore` and `evaluate` as one bundled `machine.js`, run by `bun`
  beside `code`, `git` and `restic` - every one a runtime tool the machine's
  owner binds with its closure, never an artifact the manifest pins, because a
  job sandbox has no libc and none of them ships a static build the artifact
  vocabulary could carry; the omp RPC client with a fake engine that breaks the
  wire twenty-six ways. The loop: the hub draws under the policy,
  requests jobs on the machine that holds the cited sessions, ingests
  finished outputs into the store and settles the claim at the receipt's
  cost, woken by `onJobSettled` (atyrode/manifold#510) and by the `pulse`,
  `runs` and `launch` doors; Watch's presets become `launch`, a governed door
  admitted at the operation node under the operator's consent, with a dry
  preview of the profile, model, cost and ceilings before the button. Proof:
  453 tests on a real plugin database; the bundles installed on
  `preview.manifold.tyrode.dev` (manifold `main` be79ed46) with the operator's
  own 90,719 rows crossed through the plugin's `importLedger` door, the feed
  answering 2,984 ranked records there; before that, the same on a local hub
  with a ruling recorded through the panel; and one real `scan` job
  launched from Watch, admitted, executed under bubblewrap on this machine,
  settled and ingested - exit 0 in 3.7 s, its receipt a `runs` row, 98
  sessions catalogued (job_131f45e8). Known gaps, each an issue: the beat
  cannot self-register from a hardened half (atyrode/manifold#513, #514),
  recipe bodies do not cross a job input yet (#252), and a job sees only its
  declared locations, so no session carries a repository yet (#254). #242-#246.
- **`archive` is declared, and its repository password is a service binding
  rather than a secret in a manifest.** The operation restic-backs-up this
  machine's session roots — one snapshot per root, tagged `babel`, attributed to
  the machine's own identity — and writes back the fact the catalog cannot learn
  any other way: which snapshot holds each session, and when. Neither thing that
  held it back was the code. restic ships its whole Linux distribution as bare
  bzip2, which `MachineArtifactSchema` has no format for, so it is bound as a
  runtime tool with its closure and nothing is pinned; and an operation's
  `environment` is fixed reviewed values in committed code, which is where
  neither a password nor one deployment's `s3:` locator belongs, so the
  repository, its password and the object-store credential that locator requires
  arrive together as ONE storage document from ONE service the operator installs
  per machine, `atyrode.babel.restic`. The engine materializes that binding as
  the loopback URL of the job's own service proxy and a capability minted for
  that job alone (`packages/protocol/src/jobs.ts:112-131`, manifold @
  a407d06f); the operation asks `GET /storage` once with it, and the password
  reaches restic in the child's environment and nowhere else — never argv, never
  this process's environment, never a receipt. The bearer is not the password:
  it is thirty-two random bytes per job, "a fresh job capability, never an
  upstream credential" (`packages/agent/src/job-service-proxy.ts:26-27`, same
  revision), so the secret stays behind the operator's policy and only the
  capability crosses into the sandbox. Proof: 443 tests, thirteen of them the
  operation against a real temporary restic 0.19.1 repository through a real
  loopback service — a snapshot per root, a second backup finding its parent, a
  changed session recatalogued, a refused route, a half-installed object-store
  credential, a password that does not open the repository, and a machine with
  no roots that is skipped without asking the service at all — with `pack` and
  `verify` installing the bundle, operation and all, on a real engine. Operator
  decision 2026-09-12 on #244; atyrode/manifold#515 closed with it.
- **One record, peeled.** A finding, proposal, hypothesis or observation is
  one page at `/r/<id>`, opened at its claim and expanding in place through
  five depths: the claim, the case, the evidence, the reception, the
  machinery. The first three carry no identifier at all and the fifth is
  collapsed by default, so a reader deciding whether a proposal is right
  never meets a digest, and a reader debugging Babel never has to leave the
  record to find one. `GET /api/record/{id}` serves the whole peel in one
  request and falls back to the shared catalog, which closes the defect where
  a record merged from another instance listed and then answered 404 on
  click (SPEC §8.6, #235).
- **The evidence is the hero.** Each cited locator is quoted from the
  transcript itself — the operator's own words, or the tool output the model
  read — with the speaker, the session it came from, Babel's note beneath,
  and a link that lands on the cited line. A record also says where it was
  born: the session's title, its workspace, the date, what that session cost
  and how many turns it ran.
- **The connections nobody could follow.** A record shows the other
  proposals addressing the same problem, the candidate Babel already
  suspects it restates (with the overlap it measured — computed and stored
  since the frontier existed, never served), what supersedes it, and the
  records made in the same run. Reception is tallied per review role with
  the opposing rationales readable, and *contested* now means disagreement
  within one role rather than any mixture.
- **The operator's voice, where he reads.** Agree, disagree or unsure on the
  record and on the queue, recorded as an operator-authored feedback record
  with explicit polarity; it decides nothing and the page says so in four
  words. §4.12's boundary does not move: a person still cannot author an
  assessment. The ruling — accept, reject, defer, duplicate, reopen — is a
  rule bar with a one-sentence confirm instead of a five-radio ballot.
- **The front page is a feed.** Home lists every record Babel has produced —
  hypothesis, observation, finding, proposal and the questions it asks — as
  one line with its arrows, score, kind, topics, author run, age and comment
  count, under one sort bar: hot, new, top and controversial over an hour,
  day, week, month, year or all time, and rising. Kind is a chip over the
  list, never a page; the state lives in the URL so a view is a link. A
  topic is a community — today a repository, named by the workspace of the
  sessions a record's evidence cites and inherited down the lineage, so a
  proposal sits in the topic of the observation behind it — with its own
  page at `/t/<name>` and a rail of topics with counts beside the feed.
  Navigation is Home, Mod queue (what awaits a ruling), Watch and Settings;
  Read and Ask are the feed with a filter, and every old path redirects. The
  queue keeps its tiering — a proposal outranks a finding outranks a
  candidate at equal urgency — with *why it is next* on every row. Asked for
  in one sentence by the operator on 2026-09-12 and answered by SPEC §8.7.
- **Keyboard triage.** `j`/`k` move, `Enter` opens, `a`/`d`/`u` record a
  stance without leaving the list, `r` opens the rule bar, `1`–`5` toggle a
  record's depths, `?` shows the keys, and `⌘K` opens a palette that finds
  records, sessions, subjects and open questions by name and jumps.
- **Watch is a control room.** Runs in flight with elapsed time ticking,
  records so far and a graceful Stop; a form that starts exploration,
  evaluation or the conductor on this machine under exactly the ceilings and
  refusals the CLI enforces (SPEC §8.4's deferral withdrawn, decision 90);
  thirty days of records, reviews, sessions and spend as small multiples;
  and a run page that reads a receipt as a story — what it was asked, what
  it searched for, what it fetched, what it wrote, what it declined and why,
  what went wrong, what it cost and which versions ran it. None of that body
  had ever reached the browser.
- **Sessions as data.** Cost, tokens, turns and tool errors — recorded per
  session since `migrations/0006` and dropped by the web handler until now —
  are on the wire and in a sortable table with totals; a session shows who
  cites it, and arriving from a citation lands on the cited line as the hero.
- **Ask shows reasons, not ids.** A question's rank is explained factor by
  factor, its subject is named, and a subject is one append-only timeline of
  what Babel recorded about it with the candidates that were scoped to it.
- **The deployment ranks itself.** `GET /api/feed` serves every record Babel
  has produced — hypothesis, observation, finding, proposal and the questions
  it asks — as one line of claim with its topics, its score and its comment
  count, over one sort bar: hot, new, top and controversial over an hour to
  all time, and rising. The formulas are Reddit's, stated and unit-tested in
  `internal/web/feed.go`, and they rank the whole eligible set before paging
  it (§8.5). A record's topics are the workspaces of the sessions its
  evidence cites, propagated through the development path, so a proposal
  inherits the community of the observation behind it; `GET /api/topics`
  counts them and says how many records this deployment could not place.
  `GET`/`POST /api/record/{id}/comments` is the conversation under a post —
  reviewer prose, refinements, the operator's own words, a question's answers
  — threaded by what it relates to, with §4.7's rulings beside it as the
  attributed acts they are rather than as opinions. Proof: 6,018 posts and
  twelve topics assembled from the operator's own store in 456 ms, against a
  surface that previously answered "what does every record stand at" one
  query per record (SPEC §8.7, #237).
- **A record is a post, and a post has a conversation.** The arrows are the
  vote: agree is up, disagree is down, pressing the lit one again records
  unsure, and the score beside them is support minus oppose across Babel's
  reviewers and the operator together — one number, with the breakdown one
  hover away, because a person's vote is never summed into what reads as a
  model's observation. A record with no votes at all shows an em dash rather
  than a nought. Under the five depths, `#comments` is the thread: reviewer
  contributions, refinements, answers, reconsiderations and the operator's own
  reasons, newest first, replies nested one level, with the rulings —
  accepted, rejected, deferred, duplicate, reopened — in their chronological
  place as the attributed acts they are rather than as opinions. The box
  records a reason and changes no vote (SPEC §8.7, #235).
- **A topic is a repository, not a folder.** The scan observes each session
  workspace's git identity once — the common directory every worktree of a
  repository shares, plus the origin normalized to `host/owner/repo` — and
  the feed's topics are bound to that instead of to the last element of a
  path. A session's workspace is where work happened; what it was about is
  the repository, and the difference was visible on the operator's own
  machine: of 96 sessions, a topic called `tmp` collected 32 that shared
  nothing but a scratch directory, and one project read as three communities
  because two of its worktrees were named `witty-sage-crab` and
  `bold-gold-koala`. Both are gone: the worktrees are `manifold` and the
  scratch sessions are unfiled, each carrying the reason it could not be
  filed — not a git repository, workspace absent on this host, git
  unavailable. `GET /api/topics` states each topic's binding and labels every
  filing `heuristic`, because these come from repository identity alone and
  §4.13's triage recipe has yet to revisit them. The observation is
  read-only, runs once per distinct workspace per scan, and never writes to
  a checkout (SPEC §4.13).
- **Babel proposes a topic; the operator creates it.** A topic question is
  the Reality Ledger's answer to a name a run cannot resolve (SPEC §4.8,
  §4.13): it names the entity it would create — kind, binding, aliases, why
  — the records it would file under it, and the entities it weighed and
  rejected, and it waits in the Reality Inbox with every other question.
  Accepting one mints the subject with its typed names and its binding facts
  under the accepting operator's authority and files the records the
  proposal named; declining one keeps the reason verbatim and suppresses the
  same proposal until more sessions stand behind it than there were when he
  refused. Two proposals of one repository are one question, because a topic
  question is keyed by the identity it proposes rather than by its wording,
  and an identity that already binds a live entity is refused with that
  entity named. `babel topics seed` raises one per repository identity this
  host observed and is idempotent by construction — bound, already
  proposed, or declined with nothing new to say — and `babel topics` lists
  what the operator accepted beside what is still waiting. Interest is not a
  preference knob: *working on it*, *keep an eye*, *not now* and *excluded*
  are §4.8's lifecycle and analysis-policy facts, each an attributed act
  whose reason is kept verbatim and whose predecessor is superseded rather
  than edited, so a paused project is paused everywhere Babel looks;
  retiring a topic is a lifecycle fact too, and nothing is deleted.
- **Filing is a link, and a topic is an entity.** A record's membership in a
  topic is now an append-only filing in the frontier — record to Reality
  Ledger entity, carrying its rationale and its author — published as an
  `about` edge whose kind and endpoints travel in the clear while the reason
  stays sealed with the record (migrations/0015). Re-filing supersedes,
  unfiling withdraws with a reason, and both rows survive, so where a record
  was filed and why is readable rather than inferred. `GET /api/topics` is
  three lists in one answer: the topics the operator accepted, ordered by his
  own stance — working, watching, nothing said, not now, excluded — with what
  each is bound to and how much is filed under it; the topics Babel has
  proposed and nobody has answered; and the count of records nothing has
  filed. A post's topics in `/api/feed` are the entities it is filed under and
  `?topic=` takes either a name or an id, so a repository name Babel derived
  is a proposal rather than a community until somebody accepts it — which,
  until an entity exists, makes every post unfiled, and saying so is the
  point. `POST /api/record/{id}/file` and `/unfile` are the operator's own two
  acts, and a name the ledger does not hold is a 404 that says so: filing does
  not create an entity (SPEC §4.13).
- **Not interested is a signal, not a deletion.** A topic's page states what
  the operator thinks of it — *working on it*, *keep an eye*, *not now*,
  *excluded* — and each is §4.8's lifecycle and analysis-policy facts,
  attributed to him, with the reason kept verbatim and the revision it
  replaced still readable; a topic that names two things is split, two that
  name one are merged, and one that should never have existed is retired,
  each an append-only §4.8 resolution with a required reason and nothing
  deleted. The consequence is where it has to be: the review lane now reads
  the topics a record is *filed* under, not only the names its producing run
  happened to write down, so pausing a project moves the draws off its
  records even when nothing in their wording spells its name, and excluding
  one leaves them as reported gaps rather than making them disappear
  (`POST /api/topics/{id}/interest`, `/retire`, `/api/topics/merge`,
  `/api/topics/split`; SPEC §4.13, §4.8, §4.12).
- **Babel files its own output.** A new off-by-default meta recipe,
  `babel-files-its-output`, runs under the evaluation policy as its own draw
  kind beside coverage, exploration and discovery: a tenth of a cycle
  (`filing_share`, refusable to zero) goes to records the frontier reports as
  unfiled, oldest first, and each pass files the record under a topic the
  ledger already names, proposes one the operator decides on, or records that
  it is about nothing in particular with the reason. A name no entity answers
  to is not a failure and not a new entity: it becomes a topic question, which
  is §4.8's rule that only the operator creates identity, enforced in the one
  place a run would otherwise be tempted to break it. The pass reads what the
  ledger already holds — every live topic with its aliases and binding, the
  repositories the cited sessions were in, and the reasons earlier topics were
  retired and earlier proposals declined — so Babel gets better at naming
  topics from its own history rather than from an unparsed memory prompt. Its
  receipts are ordinary receipts and name which of the three answers it
  reached; a filing is not a review, so it consumes no review cap and clears
  no coverage obligation (SPEC §4.13, §4.12).
- **Everything about a topic goes through Babel.** A new topic, a split, a
  merge and a retirement are one output kind: a *topic proposal* a run
  publishes through the ordinary chain, reviewed by Babel's reviewers, scored
  and commented on in the feed like every other proposal, and applied by the
  ruling the operator gives it — accept creates, splits, merges or retires
  and files the records the plan named; reject keeps his reason verbatim and
  suppresses the same proposal until more evidence stands behind it than
  there was when he refused. The plan hangs off the proposal record it
  explains (`reality_topic_plan`, one per proposal, immutable) and applies
  through §4.8's own acts, so a split's new part carries the identity and the
  binding the run proposed and the records that belong to it move with it,
  while a plan whose topic was merged away or retired since it was published
  is refused with the state named rather than applied against a subject that
  no longer speaks for itself. The surfaces that let a topic be changed by
  hand are gone: `POST /api/topics/{id}/retire`, `/api/topics/merge`,
  `/api/topics/split`, `/api/topics/accept`, `/api/topics/decline` and
  `babel topics seed` no longer exist, and the topic question kind with them,
  because a topic changed by hand is a change Babel did not see, cannot
  explain and cannot learn from. What the repository scan produces is
  evidence handed to the filing run — the identities this host observes and
  the ledger does not name — never a proposal it minted. Interest is the one
  direct act left (`POST /api/topics/{id}/interest`), because a stance is a
  fact about the operator rather than something Babel proposed. Observations
  leave the feed with the same reading: they are the evidence a finding
  consolidates, so `?kind=observation` is refused by name and the front page
  lists hypotheses, findings, proposals and questions (SPEC §4.13, §4.8,
  §8.7).
- **A topic change is a proposal Babel makes.** The filing pass no longer
  raises a topic *question* and nothing mints a topic from a heuristic: a new
  topic, a split, a merge and a retirement are one output kind with an
  `operation`, produced by a run through Babel's ordinary chain — a claim, the
  evidence under it, the consolidation, and a proposal titled "New topic:
  manifold" or "Split t/manifold: …" that the operator rules on the way he
  rules on every other proposal, with the ledger plan his acceptance applies
  hanging off it. The pass is shown two new kinds of material and they are
  deliberately not the same thing as the entities that exist: the repository
  identities this host observed that nothing names, with the sessions and
  checkouts behind them, are *evidence* for a `create` — and only when the
  record under review is about one of them — and the operator's own asks
  (`babel tell "topic t/manifold: …"`) are answered rather than obeyed, with
  the proposal they call for or with a reasoned no that lands as a reply on
  what he said. A target or an ask the pass was not shown is refused as a
  malformed result rather than turned into a new topic, which is §4.8's rule
  that only the operator creates identity, held at the one seam a typo could
  otherwise cross (SPEC §4.13).
- **Babel consolidates its own backlog.** The hypotheses a run deferred and
  nobody came back to are worked through the chain rather than left to
  accumulate: a second recipe beside *Babel files its output*, *Babel
  consolidates its backlog*, runs under the evaluation policy as its own draw
  kind with its own reserved tenth, reads one deferred candidate with its
  observations, the candidates beside it and the ledger's entities, and
  proposes — as an ordinary proposal through the ordinary chain, ruled on the
  way every other proposal is — to consolidate several candidates into a
  finding, to supersede one with a newer candidate that says it better, to
  retire one with a reason a reader could check, or to promote an observation
  to a fact about a named entity. Keeping a candidate exactly as it is, with
  the reason, is the fifth answer and a completed pass. Hypotheses gain
  `superseded` and `retired`, reachable only through an accepted proposal and
  revivable like every other resting status; a candidate in either is no
  longer awaiting the operator and is no longer drawn for review. Nothing is
  deleted: every settled candidate keeps its record, its observations and its
  history, and gains one appended status event saying which record now speaks
  for it (SPEC §4.13).
- **The catalog comes from a job, not a command.** The plugin's machine half
  ships its first operation: `scan` walks this machine's session roots and
  writes one `sessions` row per session as a job output the hub ingests —
  source identity, the harness's own recorded title with its provenance, the
  workspace, the modified time, the bytes and their `sha256:` digest, and for
  OMP the spend the transcript itself recorded. The repository identity is
  observed once per workspace from `git rev-parse --git-common-dir` with
  `GIT_OPTIONAL_LOCKS=0` and a one-second bound, so a checkout and every
  linked worktree of it file under one project, a normalized `origin`
  (`host/owner/repo`) names it across machines, and a workspace that is not a
  repository carries the reason instead of a guess. The three source adapters
  (omp, codex, claude) are ported to TypeScript with their identities
  unchanged, so imported provenance still matches what a scan finds; Codex's
  offline title derivation comes with them. Proof: `bun test
  plugins/atyrode.babel/machine` (47 tests), and one read-only run over the
  operator's own `~/.omp` catalogued 98 sessions, 1.04 GB, in 8.6 s — 58 of
  them filed under 7 repositories, 35 in directories that are not
  repositories and say so (plan §4, #244).

- **A drain is a budget overlay with a TTL, never an edit of the standing
  policy.** `setBudget({ expiresAt, concurrentPerMachine?, perCycleCost?,
  dailyCost?, reason })` writes one `budgets` row and `clearBudget({ id,
  reason })` ends it early, with both instants and both reasons kept; nothing
  unwinds an overlay, because the newest unexpired uncleared row is simply the
  one in force. The standing `policies` row is byte-identical before and
  after, so the `policyVersion` every in-flight assignment id digests does not
  move — on 2026-09-13 five policy rewrites minted a second id for reviews
  already in flight, and eval-policy-10's batch of 256 outlived the drain it
  was raised for by ninety minutes. The bound is now per machine
  (`concurrentPerMachine`) and it is the ONE knob an overlay turns: admission
  caps the deployment at that bound across the machines a cycle finds online,
  names what each holds when it refuses, and never counts a machine that has
  gone offline against the ones still running. What an overlay may name is
  bounded in turn by the manifest's `limits.concurrentJobs` (16 on explore and
  evaluate), because the hub refuses every posting past it at `execute` and a
  refused posting costs its reservation and produces no review; the refusal
  names the ceiling, and a posting the hub does refuse is reported with the
  hub's own word for it. Watch shows the overlay as a strip beside the
  standing figures — what moved, from what, and for how much longer — never
  instead of them (#260, #281).

### Changed

- **Publication needs nobody.** `babel web` drains the machine's journal at
  startup and every minute while it serves, and every run drains once on
  exit, so a run killed before declaring its closure is sealed and published
  by the next run that finishes rather than by someone who noticed. Measured
  on 2026-09-12: a workstation running lanes with no conductor stranded 300
  records behind 379 undeclared closures until `babel sync` was typed by
  hand. `babel sync` is a diagnostic again (SPEC §9.1).
- **The surface has a register.** Editorial where a record is read — a
  bundled serif for the claim, a bounded measure, quoted evidence — and
  observatory where Babel is watched: tabular figures, sparklines, live
  state. Thirty-seven card classes collapsed to `surface`, `panel` and
  `quote`; badges are rationed to standing and kind; the fallibility
  disclaimer is said once, in the footer, instead of on every record. The
  Manifold plugin framing no longer constrains the surface (SPEC §2.8,
  decision 90).
- **The machine that produced something is not a dimension of reading.**
  Host tabs, host chips and host sorts are gone from Watch and Sessions; a
  host appears only under Settings › Archive, where a snapshot is a backup
  of a machine.
- **A review that takes longer than its lease keeps its claim.** A lease bounds
  how long an unanswered worker holds an assignment, not how long the work may
  take, and the two were conflated: the claim lapsed while the model was still
  reading, and the result was then refused at the end for a takeover that had
  never happened. Measured on 2026-09-12: four reviews ran 386s, 461s, 556s and
  630s under a 240s lease, every one of them was refused its own claim, and the
  deployment recorded no reviewer vote at all. A worker that is still working
  now says so, renewing at a third of its lease for as long as the review runs,
  and the shared catalog admits the renewal it used to refuse — under
  `migrations/0014` a live claim may move its own expiry forward and nothing
  else, so an expired assignment is still taken over under a new fence rather
  than resurrected, and a renewal charges nothing further against the day.
- **A preparation never reads a file that is still being written, nor Babel's
  own transcripts by accident.** A session's catalog row now says two things it
  could not before: `live`, when its log was written inside the last two
  minutes, and `kind`, whether the conversation was the operator's or one of
  Babel's own runs'. Both are observations — a live session and a run's
  transcript are still scanned, catalogued and archived like every other — and
  what honours them is every place a corpus is chosen: `prepare` and the Watch
  presets' inline selection skip them, and a preset that studies Babel asks for
  the agent ones with `agentSessions`. Measured on 2026-09-13: twelve
  concurrent draws each re-read one 240 MB Code session the operator had open,
  its mtime moving the whole time, and all six `babel explore` runs of that day
  reported "changed since the preparation was fixed" over a 35 MB log that was
  the harness session running the drain itself. Ten preparations built while a
  session is appended between each now produce one identical selection digest,
  because the file none of them read is the one that was moving. #262.
- **The plugin builds against Manifold `main`, and an INTEGER column is a
  BIGINT.** `plugins/MANIFOLD_REV` and the gate's `uses:` ref move together to
  `637cbb79`, Manifold `main`, which carries the plugin database's
  failure-atomic lifecycle (atyrode/manifold#536), per-operation
  `concurrentJobs` admission (#551) and the `job_progress` event (#552) — the
  two primitives the drain epic asked Manifold for — and metered brokered
  inference (ADR 0038, #554). Since #536 the engine opens a plugin's file with
  `safeIntegers` (`packages/server/src/plugin-database.ts:163`), so every
  INTEGER column and every `lastInsertRowid` answers as a BIGINT, and three
  places read one as a number: `withdrawn === 1` was false for a withdrawn
  filing, so a second `unfile` appended a second withdrawal instead of being
  refused; a citation's `position` reached a job request as a value JSON
  cannot carry, so a review of a record that cites a session could not be
  posted at all; and the reaper's `COUNT(*) === 0` never matched, so a claim
  whose job left no run row was never abandoned and held its batch slot to the
  end of its lease. Each is coerced where it is read, the two fakes open their
  file with the options the engine opens the real one with, and every
  assertion about a stored row now says what the database returns — `2n`, not
  `2`. Proof: the plugin gate against the pin — `check` clean, 485 tests,
  `pack` and `verify` on a real engine spawned from the pinned checkout.
  `AGENTS.md` stops saying plugin work is paused: the plugins are the product
  under construction, moving the pin to a newer `main` is ordinary work in its
  own PR, and the gate runs on every PR touching `plugins/` (operator
  direction 2026-09-13, #268).

### Fixed

- **One validator for a review submission, and a refused review is spend.** The
  rule about scope — an outcome or a criterion result needs the setting it was
  observed in and the time it was observed at, and a setting with no claim
  about it scopes nothing — was stated twice in the plugin and told to only one
  of the two roles that can break it: `machine/engine/results.ts` refused a
  criterion result with no environment, the prompt said so to the outcome role
  alone, and the store wrote whatever an `assessments` row carried. The Go tree
  stated it three times and the three disagreed, which is how the drain of
  2026-09-13 paid for evidence-role reviews and had them refused at submit: an
  evidence check is exactly a criterion result with no outcome
  (`docs/postmortem-2026-09-13-drain.md`, F8). `acceptReviewResult` is now the
  one acceptance — the engine's per-role JSON Schema is generated from the same
  field table, the prompt interpolates the single sentence of the rule, and
  `store/acts.ts`'s `refuseRow` runs it over the payload of every assessment
  the hub ingests, so a producer and the store cannot disagree about one
  payload. A refused submission now carries its own refusal code into the
  receipt — `schema`, `support` or `empty`, not one `result-schema` for all
  three — beside what the run cost, so the loop finishes the claim with the
  money the refused review actually spent. Proof: an evidence result with
  `results` and an `environment` and no `outcome` is accepted by the validator,
  written by a real run against the fake engine, and accepted by the store from
  the row that run wrote; a contribution with an environment alone is refused
  `schema` on both sides, under the same code; a refused submission's receipt
  carries `schema:` and the engine's own $0.0123 (#263).

- **A claim dies with its job.** A review's claim held its batch slot until its
  lease expired even when the job behind it was gone, so a worker killed by
  hand kept the deployment from drawing the record it had been holding: on
  2026-09-13 about seventy such ghosts, under leases the operator had raised to
  5,200s, answered every draw of a two-hour drain with "held by another worker
  until 14:08" and the window it existed to spend was lost. The plugin
  coordinator now has `abandon`, which finishes a claim as `abandoned` and
  charges what it reserved — a job that died mid-review cannot say what it
  spent, and releasing it at zero would let a crash loop spend the day's
  allowance many times over — and the conductor calls it wherever a job ends
  without a result: a settlement that read no receipt on a job the hub did not
  report as a clean exit, a posting the machine refused (the claim is taken
  before `jobs.execute` is called, so a refusal used to leave a grant with no
  worker at all), and a reaper on every cycle for grants never posted, jobs
  with no open run row, and jobs the hub cannot report twice running. A claim
  with no job is no longer counted as a batch slot at all.

  Underneath it, the reason no claim was being settled at all on a real hub: the
  engine's database answers an INTEGER column with a BIGINT, every caller of
  `finish` reads the fence out of a query of its own, and `1n !== 1` refused the
  caller the claim it was holding — silently, because to all three of them a
  refusal is nothing to do, so an operator's `stop` closed the run and left the
  batch slot held. A fence is now taken in either shape and normalized once, in
  the store that owns what a fence is. Proof: 66 tests, among them four killed
  jobs whose claims are abandoned and whose batch admits the next draw on the
  following tick, and the two settlement tests that ran against a real plugin
  database and had been failing (`stop cancels the job, closes the run and
  releases what it reserved`; `a settled job of this plugin's ingests what
  finished`) (#259, post-mortem finding F3/G1/O5).

- **Paid-but-refused work is spend, not a free failure, and a cycle says why it
  drew nothing.** The conductor parks itself after three settlements in a row
  that reached no model and produced nothing, and what counts as one of those is
  the whole point: a review the model answered and the contract then refused
  (`schema`, `support`, `empty`) is money the day's allowance already paid, so
  it is spend, while a job that died before its first call and a claim abandoned
  because its worker never came back are the free failures a park exists to
  stop. The Go conductor counted the first as the second — at 10:36 on
  2026-09-13 it stood parked after three failed cycles with evaluation ladder
  6,022 never reviewed, a scripted fan bypassed it entirely, and the unreviewed
  count grew from 6,024 to 6,038 while the window it existed to spend drained
  (`docs/postmortem-2026-09-13-drain.md`, F16/F8/G9). The streak is read off the
  claims ledger per policy version rather than counted in a process, because
  every cycle is a fresh loop over the wake that caused it; an hour of quiet
  lifts a park without an operator and a new policy clears it at once, and a
  parked loop is the launch door's answer too, since there is one loop and one
  park. Beside it the cycle report gains the two counts nothing answered before:
  `gaps` by the coordinator's own reason (`batch`, `per-cycle`, `daily`,
  `no-candidates`, `claimed`, and `disabled`, which the loop counts itself) and
  `refusals` by the code `machine/engine/results.ts` names, each for the tick
  and cumulatively for the UTC day. Proof: three refused-after-paid reviews
  whose receipts report no cost at all leave the loop drawing and are reported
  as `{schema: 3}`; three interrupted jobs park it, which then asks the
  coordinator for nothing and draws again an hour later; a draw answered `batch`
  reports `gaps.batch === 1` and names the reason, adds up over a day and starts
  again at the boundary (#265, post-mortem finding F16/F11/G9).

- **A run says where it is and what it has spent while it is still running, and
  its receipt keeps both.** Between `started` and a terminal state a Manifold
  job carried only the hub's own facts, which say nothing about the work: on
  2026-09-13 a run printed `preparing N/M` and then nothing for the rest of its
  life, and seventy-five minutes passed with no engine on the machine and
  nothing anywhere saying so, while the tokens every receipt already held were
  never read (`docs/postmortem-2026-09-13-drain.md`, F12/F19/O2). The machine
  half now reports three stages — `preparing` with a fraction where its loop
  counts, `at the model` from the instant the prompt is written, `submitting` —
  on the private owner channel manifold#552 gives every job, and the conductor
  folds them with the owner's metered `inference_call`s into one row per
  in-flight run: the stage and its own clock, the calls, the input, output and
  cache tokens, the spend and the model that answered last. It is read through
  `follow`'s snapshot, taken and closed in the same turn, because the hub
  refuses a running job's journal (`job_unfinished`) and keeps that snapshot's
  ring for every job whether or not anyone watches; a cycle a settlement woke is
  served no `follow` and says nothing rather than guessing. Watch shows the row,
  says how many runs are at the model, and marks a metered run that has had no
  call for ninety seconds `stalled` — never an unmetered one, where that silence
  is the ordinary state and not a symptom. At settle the owner's meter fills the
  run's tokens and cost in preference to the engine's own account of itself, and
  the whole of `usage.inference` is kept beside the receipt, so the calls and
  the cache survive the in-flight row being dropped. Proof: 17 new tests, among
  them every stage parsed against the pinned `WorkerProgressSchema` and a frame
  the owner would refuse never written, a real explore run reporting the three
  stages in order, a fold over a fake ring that reads two calls, 12,400 input
  tokens and the model that answered last and closes what it opened, a settle
  that keeps the meter's three calls and 21,500 tokens with the receipt, 89
  seconds being a slow turn where 91 seconds is a stall, ten minutes of an
  unmetered run at the model being neither, a settlement-woken cycle folding
  nothing and saying nothing, and a job that has said nothing having no row to
  read (#261, post-mortem finding F12/F19/G5).

## [0.2.6] - 2026-09-12

### Added

- **Babel reviews its own backlog, and the browser reads what it found.** The
  approved full-lifecycle evaluation system is now runtime behaviour: a worker
  draws one claimed, role-bounded review at a time under a versioned operator
  policy, forms a bare vote or a substantive contribution against one exact
  revision, and records it as an attributed, append-only, published record.
  Blinding is procedural — the served projection, the job parameters and the
  tool schemas all withhold prior evaluations — and a run may correct its own
  statement only through a follow-up assignment reserved before the second pass
  runs. Coverage is a first-class inventory: never-reviewed, reassessment-due,
  overdue, unsupported, blocked and not-applicable are distinct, a bare vote
  cannot satisfy an evidence check or an outcome verification, and a skipped or
  unpriced attempt stays a visible gap rather than becoming a negative vote.
  The conductor gains a protected evaluation share (`--evaluate`,
  `--evaluate-cadence`) beside `babel evaluate`, and `/evaluation` serves the
  ranked backlog, the coverage inventory, the policy form and one record's
  full history. Spending is fleet-wide: PostgreSQL now carries evaluation
  claims and a per-day allowance, claims are fenced, unobserved spend is
  charged at its reservation rather than assumed free, and a reconsideration
  decision states `reopen` or `retain` explicitly — reopening writes the
  disposition in the same transaction as the decision, and retaining changes
  nothing about the earlier ruling.

### Changed

- **Proposal triage is retired as a standing duty.** The version-1 duty ranked
  unruled proposals within a cohort and demanded a counterargument for every
  piece of advice. Its recipe is now version 2, its authorization toggle
  (`--babel-triages-the-queue`) authorizes the evaluation share instead, and
  historical v1 advice stays readable as advice: no rank is converted into a
  vote, an exposure or a prediction.

## [0.2.5] - 2026-09-11

### Changed

- **The dashboard loads on a machine that did not produce the records.** A
  merged listing opened the deployment's records one at a time, and dropping
  this machine's share before opening hid what that cost: the instance that
  made the records skipped nearly all of them, while an instance that made
  none skipped nothing and paid an object-store round trip, a digest check
  and a decrypt for every row, in sequence. The deployment's 1,958-candidate
  frontier took 31.7s read that way, against the browser's own 20-second
  abort, so the dashboard said it could not be loaded on every machine except
  the one that happened to have made the records. The records a listing keeps
  are now opened together, bounded by the publisher's own worker count, which
  took the same read to 6.2s; a cancelled request is reported once instead of
  as a page of records each claiming to be sealed. Measured against the live
  catalog with `BABEL_HOST_ID` naming a machine that owns none of it.

- **Babel releases its own binary and nothing else.** The manifold plugins are
  paused: Babel is a standalone product whose interface is `babel web`, whose
  storage is PostgreSQL and restic, and nothing it does depends on a hub. The
  integration was scratched before manifold had the primitives it needs, and
  carrying it in the release path meant a green build reported failure —
  v0.2.4's binaries published while its preview install refused with
  `artifact_invalid: server module must default-export a ServerPluginDef`, a
  loader contract written after `plugins/MANIFOLD_REV`. The packing and preview
  jobs are gone from the release, the plugin gate runs only on
  `workflow_dispatch`, and the sources stay in the tree with what is stale
  recorded at the head of `plugins/README.md`, because the work is postponed
  rather than abandoned.

## [0.2.4] - 2026-09-11

### Added

- **The archive drains itself, like every other record.** §9.1 requires every
  record Babel produces to reach the shared catalog without an operator
  action, and the archive half broke that in two places. `uncatalogued` — a
  snapshot restic holds that the catalog has no row for — self-healed only if
  the snapshot's own host pushed again, because adoption filtered the
  repository listing to the pushing host and the catalog refused anything
  else, so a snapshot stranded by a machine that was retired, died, or was
  merely idle stayed outside the catalog indefinitely. `catalog-pending` — a
  row carrying restic's real counts and no record of which sessions the
  snapshot held — had no resolution at all: `archive status` told the operator
  that no shipped command resolved it and that the count would not fall. Both
  now drain as part of `babel archive push`, which the hourly timer already
  runs, and neither is behind a new command. Any pushing host adopts every
  unknown snapshot in the repository, recording each under the host restic
  named and under that host's own `publication_order` — never mixed between
  hosts, and written only while holding that host's publication lease, so two
  instances pushing at once cannot number one snapshot twice. A snapshot that
  names no host is still refused, because its identity is unknown rather than
  absent. And a `catalog-pending` snapshot is completed by restoring it to a
  disposable area under the cache directory, rescanning it with the same
  adapter discovery and describe a normal push uses, publishing the session
  rows it actually held, and marking it committed — recovering the same
  session identities its owning host had published, since the digest is over
  the owning host and not the restoring instance. The rescan is bounded at two
  snapshots per push so the hourly timer stays bounded, spends that bound
  round-robin across hosts so one machine's backlog cannot starve another's,
  removes its restore area on success and on failure alike, isolates one
  unrestorable snapshot from the rest, and writes nothing to the repository. A
  session row a later push already wrote is never rewound to what an older
  snapshot held. Both drains report themselves in the push summary and in
  `--json`: `snapshots adopted`, `snapshots completed`, `sessions recovered`,
  and `snapshots unrecovered` for the rows that could not be read and will be
  retried. Two residues remain and are stated rather than implied, both about
  harnesses. A snapshot holding only a harness this binary does not read
  completes with none of its sessions, exactly as an ordinary push of that
  machine would, because the harness set is `internal/harness`'s single
  declaration. And a snapshot holding a session whose harness the catalog's
  own `sessions.harness` enum does not admit is refused rather than completed
  short of it — `babel` itself is such a harness today — because a
  `session_count` that included a row the schema cannot store would be a
  number no reader could reconcile; widening that enum is a migration against
  a frozen schema, not a change here.
- **Recorded focus reaches the loop.** The ledger has been able to hold "stop
  spending on this project" since §4.8's focus rules were implemented, and
  nothing read them: the conductor picked candidates straight off the
  frontier and `babel prepare` scoped every session it found, so an operator
  could record the intent and watch it have no effect. Selection now
  evaluates the allowance for each candidate's subjects, and preparation for
  each session's. The four values stay four: `excluded` permits nothing,
  `no-code-investigation` permits only synthesis over material already held,
  `learn-only` keeps the subject's sessions in the corpus so it can still be
  mined for cross-cutting lessons while withholding work about the subject
  itself, and `full` is unchanged. `babel reality focus install` installs the
  rule set this build ships, without which nothing is withheld because
  nothing has been stated.
- **A subject can be named from the browser.** Every fact, Question and focus
  policy is about an entity that already exists, and the only way to make one
  exist was `babel reality entity create` — so the focus page's answer to a
  name it did not recognize was a sentence telling the operator to go and use
  a terminal, which §8.4 counts as a product that does not have the thing it
  stores. `POST /api/reality/subject/create` writes the same record that
  command writes: the entity, its first membership entry, the fleet's claim on
  it, and each typed alias, with the ledger's own validation and the ledger's
  own closed vocabularies served to the form by
  `GET /api/reality/subject/vocabulary` rather than copied into it. It is
  reachable from the Subjects listing, including its empty state, and directly
  from the focus page's unresolved-name state — which now carries the word the
  operator typed into the form, records it as a typed name, and re-resolves it
  afterwards, so the dead end became the next step and the policy he came to
  state is one click away.

  The authority is the operator's own, resolved by the same launch identity
  every §4.7 and §4.8 mutation on that surface uses, and a session that cannot
  name an operator creates nothing. It is a third reality surface rather than
  two more methods on the two that exist: `reality.SubjectNaming` can create an
  identity and attach names to it and has no method that writes a fact, so a
  page that can name a thing still cannot make Babel believe anything about
  it — creating a subject asserts nothing, and the response says so. A name the
  ledger already resolves is refused with the identifier of the subject that
  holds it rather than duplicated, because two subjects for one thing is the
  mistaken identity §4.8's merge history exists to undo; a name that already
  means several subjects is refused without offering to name a third.
- **A skipped candidate says why.** Withholding a consolidation cycle writes
  the immutable context snapshot §4.8 requires a deterministic deferral to
  leave behind — the policy version, the resolved entity, the rule and the
  facts it matched — and `reality.Snapshots` reads them back by candidate.
  Nothing is deleted and nothing is rewritten: the candidate keeps its place
  on the frontier and its wording, `conductor status` reports how much of the
  backlog focus is holding, and superseding the fact makes it drawable again
  on the next cycle with nothing to restore.
- **Babel records its own analysis runs as sessions.** An exploration's
  reasoning used to live only as long as the process that produced it: the
  receipt records the profile, the grant, every tool call and Babel's decision
  on it, and deliberately not the transcript, so a finding could be reopened
  through its locators months later while the argument for it was gone. Each
  supervised job now writes its conversation to
  `$XDG_DATA_HOME/babel/analysis/<run>/<job>.babel.jsonl`, and a fourth source
  adapter (`babel`) discovers, describes and renders those logs exactly as the
  three harnesses' — so `babel sessions list --harness babel`,
  `sessions inspect`, the web session view and `archive push` all carry them
  with no storage configuration change on any machine.

  What a session log may *not* hold is the corpus. A retrieved excerpt reaches
  the model because a model that cannot read a record cannot form an
  observation about it, and it comes straight back in the engine's own
  `agent_end` message list; persisted verbatim it would be a second plaintext
  copy of the archive under Babel's data directory, which SPEC.md §9 forbids
  of every durable record. So the writer reduces every served tool result to
  the locators that recover its bytes and the reason the content is absent —
  the asymmetry the receipt's retrieval trace already keeps — while the job
  document, the model's reasoning, its tool calls and its conclusions are kept
  verbatim. It is fail-closed in both directions: a writer cannot be
  constructed without a redactor, and a payload the facility does not
  recognise is withheld rather than copied. A real run over a synthetic corpus
  proves it, and the guard fails if the reduction is removed.

- **Babel reads its own proposals before you do.** A new off-by-default meta
  recipe, `babel-triages-the-queue`, takes the proposals nobody has ruled on
  and records advice beside each one: which of them say the same thing, which
  is worth reading first and why, the case against acting on it, and — where
  it has one — a better-stated alternative. The advice appears on the review
  page, directly above the decision it is for, because advice a person has to
  go and find arrives after the decision it was written for. Authorize it with
  `babel conductor configure --babel-triages-the-queue`.

  It may rank, cluster, weigh and re-propose. It may not rule. That is a
  property of the types rather than a promise in a comment: the triage pass
  holds a narrow `*frontier.Triage` handle with three methods and no path to
  `Decide`, `RejectAndRefine`, `SetStatus` or `DeferFrontier`, and the store
  refuses advice on a proposal a ruling has already been recorded against. An
  alternative is a new proposal record resting on exactly what the original
  rests on — same support, same claims, different wording — so the original
  keeps its id, its wording and its place in the queue, and the operator
  chooses between two records. Whether Babel may ever accept or reject on its
  own is deliberately still unanswered, and nothing here anticipates an answer.

  The proposals listing now says which rows Babel has already read, so the
  advice is findable by scanning the pile rather than by opening records one
  at a time to see whether any was left. The mark is presence and nothing
  else: the rank is deliberately not served to a listing, because a queue
  that could sort on Babel's suggested reading order would have done the
  operator's triage instead of offering to help with it. It sits with the
  record's own text rather than in the Review column, which is where a ruling
  goes, and a proposal no pass has read carries no mark at all.

- **Findings and proposals have a front door.** Both are the first entries in
  the navigation, proposals being an entirely new listing — before this,
  Babel's committed output was reachable only by guessing a URL. The dashboard
  leads with what still needs a decision instead of a total that is 89%
  deferred.

### Changed

- **A harness is declared once.** Teaching Babel a fourth harness took five
  unrelated edits — the event scanner's classifier, the transcript view's
  parser, the preparation validator's name list, the CLI's adapter list, and
  the adapter port's documentation — and a missed one failed at run time
  rather than at build time: that is how `unknown harness "babel"` reached a
  running conductor cycle and ended it. `internal/harness` now holds the set,
  and a harness is a name plus the record language its primary log is written
  in; the scanner, the session view, the preparation validator and the
  adapter list all resolve it there instead of each keeping a list of names.
  Registering a harness whose records are in a language Babel already reads
  is one line and no second edit, and a declared harness that no reader or
  source adapter covers is a failing test rather than a refusal mid-run.

  SPEC.md gains the rule the locator has always implemented. §4.11 states it:
  derived output carries locators, never copied text, so every stored
  artifact is a statement about bytes that exist elsewhere — resolving down
  to a commit, a file and line, a repository, or a located excerpt of a
  transcript — which is why Babel analysing its own output cannot amplify
  duplicated material and why an archive of everything stays proportional to
  what was observed. §6.8 specifies the pipeline as harness-agnostic in both
  directions, original log through forward transformer to canonical model and
  back, with omp, codex and claude named as the first adapters rather than
  the set; a resume must rehydrate from the retained capture rather than
  synthesize a log from the canonical model, since the canonical model drops
  harness-specific structure by design and a 95%-faithful resume is worse
  than none. Reverse transformers are specified and unimplemented, gated in
  §14.

- **The review page shows what it is asking about.** Deciding on a proposal
  meant reading a hex id and pressing a button labelled RAW PRIVATE VIEW; the
  record itself is now the page, headed by its title and broken into the
  problem, the proposed outcome, how you would know it worked, what could go
  wrong, and what is still unanswered. Evidence locators resolve to anchors
  that open the cited transcript *at* the citation rather than at record 1 of
  several thousand, and stored whitespace escapes render as whitespace.
- **The machine left the reading path.** Host columns, host filters, host
  sorting and `local` / `pending-sync` / `committed` chips are gone from every
  reading surface except Archive, where a snapshot genuinely is a backup of
  one machine. Fleet keeps its route but leaves the primary navigation. The
  Sessions page loses the last of it: no host in its heading, no scope
  selector, and no cross-host archive table whose columns said only that
  nothing had looked. It lists the corpus by time and by each session's own
  attributes. Recovering a session out of another machine's snapshot stays
  `babel sessions fetch --host`, and the Archive page still reports snapshots
  per host.
- **Rulings open.** Two packages publish `KindDisposition`, and the reader
  handed both to the frontier decoder, which requires a `schema` field a
  disposition's wire form does not carry — so every accept or reject read back
  as `published record <id> carries no schema version`. Records now route to a
  decoder on their own bytes, as published edges already did.
- **Listings read the whole deployment by default.** A record published from
  any instance appears in findings, proposals and hypotheses without asking;
  `?fleet=0` narrows to this machine. A shared-catalog outage degrades a
  listing with a notice instead of refusing it, so losing the network never
  costs the operator the ability to read his own work.
- **The dashboard stops reading the corpus to count it.** Its frontier panel
  enumerated up to five thousand candidate identifiers and then read each
  candidate individually, so on a 1,958-candidate store `GET /api/overview`
  took 31.5 seconds — past the browser's 20-second abort, which discarded the
  whole document and left every panel empty while the four sections behind it
  logged `context canceled`. The panel now asks the frontier for what it
  shows: one count per §4.2 status and one page of the newest candidates,
  seven bounded queries whatever the corpus holds. The total is the store's
  own aggregate, so it is `babel hypotheses`'s total and includes the
  superseded revisions and resting candidates the old enumeration could not
  reach.
- **The frontier listing and the dashboard count one frontier.** The
  hypotheses page enumerated internal/review's queue unioned with the
  unexplored frontier, which was the only listing the store offered when the
  route was written: a superseded revision and a candidate that came to rest
  without being enrolled were reachable by identifier and by no listing at
  all, so the page showed fewer candidates than the dashboard beside it
  counted. The page now pages the store's own enumeration — the one `babel
  hypotheses` lists — and narrows by status inside the query rather than by
  reading every record to find out. A candidate another instance published
  and this one cannot open is counted by the panel as well as listed by the
  page, and is placed in no status, because it has none this instance has
  read.
- **Concurrent cycles stop colliding in the frontier index.** Each cycle opens
  its own handle on the index, and the reconcile read what the index already
  held *before* opening its write transaction, so two cycles could both see a
  record absent and the second insert failed `frontier_records.record_id`'s
  UNIQUE constraint. The cycle was reported degraded after its model work was
  already paid for; observed live under `--concurrent 3`. The snapshot is now
  read inside the transaction, which `durable.DSN` already begins IMMEDIATE,
  so the write lock is held before the check.
- **A run can ask.** A stage's result may now carry questions, and Babel
  resolves each subject through the ledger's aliases and raises it into the
  prioritized inbox — the third way a fact can come into existence, after an
  operator's own edit and a trusted source's batch. It is the narrowest seam
  that is useful: a question authorizes nothing, so a run may raise one and
  may never answer one, and a subject the operator has not declared is a
  recorded refusal rather than a new identity. `TestARunRaisesAQuestionInto
  TheInbox` runs the real path against a real ledger and asserts the ledger
  gained no facts.
- **A loop that cannot run stops running.** Three consecutive cycles that
  fail without spending anything park the conductor instead of drawing more
  work. Cost is the evidence rather than the failure's text: a provider
  window at its limit, a worker pin naming a binary a system rebuild removed
  and an engine refusing the profile all look identical from here, and all of
  them otherwise spin at several cycles a minute for the rest of the window.
  A cycle that reached the model and then failed resets the count.
- **The dedup probe can see the run's own work.** The frontier index is
  refreshed before a run starts, so a candidate written by this run's explore
  stage was invisible to its challenge stage's duplicate check — measured on
  the real store, two runs restated their own candidate at 0.61 and 0.71
  overlap and neither restatement carried a warning. The probe now measures
  against the statements this attempt has already persisted as well as the
  indexed heads.
- **The self-improvement recipe can name a contract defect.** Its
  classifications covered recipe prose, plumbing and code, so a defect in
  `SPEC.md` itself had to be filed as one of the three things it is not.
  `babel-improves-babel` version 2 adds `contract-defect`.
- **The Reality Ledger has a way in.** Entities, facts, trusted sources,
  Questions, the interpreter plan gate and the prioritized inbox all shipped
  and were tested, and every `reality_*` table on every machine held zero
  rows: `CreateEntity` and `RegisterTrustedSource` had no caller outside the
  package's own tests, so a fact could not be imported because its subject
  could not exist, and §4.8's "one versioned inventory import" had never been
  performed. `babel reality entity create` and `babel reality source
  register` are the missing acts, in the order seeding needs them.

- **Babel asks its first Questions.** A predicate carries a refresh
  expectation — where a service runs is worth doubting after a month — and
  the ledger marked facts stale without telling anyone, because a Question
  could only exist if an operator typed one. `babel reality refresh` expires
  what has lapsed and raises one maintenance Question per stale fact, naming
  the subject, the predicate and the lapsed fact as its evidence. It writes
  no fact and needs no model: a Question authorizes nothing, which is why
  analysis may raise one and only the named authority may answer it. Repeated
  passes add nothing while a question is already open, and a declined one
  stays declined until newer evidence arrives.

- **A stage's prompt is ordered so a provider can cache it.** The parameter
  block naming the run sat in front of the recipe bodies, and the stage
  heading sat in front of everything, so a prompt shared almost nothing with
  its neighbours: two runs of one stage shared 5,598 bytes, and the three
  stages of a single run shared eight. Every stage of every run therefore
  paid to write the whole cookbook into cache again — a real overnight batch
  spent 5.2M cache-write tokens against 27M reads, and writes are the
  expensive half. The invariant part now leads (recipes, then the answering
  protocol), the stage's own instructions and tools follow it, and the run's
  parameters, sources and prior records come last. Two runs of a stage now
  share 84,573 bytes of 84,849, and the three stages of one run share
  213,996 of 218,887.

- **The conductor draws from the fleet's corpus.** `sessions fetch-all` and
  `--fetched` let a person scope another machine's sessions, but the
  unattended loop still sliced only its own host's sources: 343 fetched
  sessions sat on disk while every cycle redrew the same 88 local ones. The
  serendipity floor and the resolver that turns a draw into a run now read the
  same fleet-wide corpus, so an overnight loop covers the archive rather than
  the machine it happens to run on.

- **The conductor consolidates, runs cycles concurrently, and publishes at the
  cycle boundary.** A loop left alone grew the frontier and never returned to
  it, ran one cycle at a time whatever the ceilings afforded, and kept every
  record local until it was stopped — so a machine running for days was
  invisible to the fleet and its accumulated candidates were never developed.
  `--consolidate N` draws the unexplored candidates one cycle in N as a
  protected share reported after the ladder, `--concurrent N` runs cycles
  against one serialized budget claim so the day's ceiling binds on what is
  committed rather than on what has already reported, and each cycle publishes
  its own records when it ends.

- **Analysis can read the fleet's corpus, not just this machine's sessions.**
  `babel sessions fetch-all (--host HOST | --all-hosts)` restores every session
  a host archived — concurrently, resumably, and recording one failure per
  selector rather than abandoning the batch — and `--fetched` widens
  `sessions list`, `sessions inspect` and `prepare` to that corpus. A fetched
  session is attributed to the machine whose snapshot it came from rather than
  to the machine that restored it, so a cross-host scope reports where its
  material was actually produced.

- **`babel conductor run --challenge --synthesize` lets the loop consolidate
  what it explores.** A cycle ran the discovery pass and nothing else, with no
  way to authorize otherwise, so an unattended loop could only grow the
  hypothesis frontier: the synthesizer is the sole writer of findings and
  proposals, and it was unreachable from the conductor at any setting. Both
  stages stay off by default because each is a separate worker job billed
  against the same ceiling, and `--synthesize` without `--challenge` is
  refused — §5.4 promotes nothing a skeptical pass has not attacked first. An
  authorized cycle against the synthetic engine runs three worker jobs where
  an unauthorized one runs one.

- **Repository instructions separate shared policy from Babel-specific guidance (#193).**
  The root keeps useful checks, generated-artifact ownership and operator-only archive,
  custody and deployment boundaries, while routing detailed procedures to their owners
  only for relevant tasks. Reusable engineering rules come from the harness-neutral
  dotfiles source; checked maintenance PRs update only the generated block, with exact-byte
  and outside-block preservation checks protecting the local instructions.

- **Interrupted runs have durable checkpoints and explicit recovery commands
  (#176).** `babel runs interrupted`, `reconcile`, `resume` and `close` distinguish
  observed interruption from unknown process loss, retain the producing run's
  identity and trusted launch inputs, and publish partial results without
  reopening immutable closures. A stop file requests a cooperative safe-point
  stop. Conductor recovery finalizes a completed receipt's unfinished cycle
  without launching inference again; regression coverage checks preserved spend,
  outcome and identity, as well as remedy deduplication across a real interruption.

- **Run receipts retain observed native assistant accounting (#169).** Actual
  provider/model responses, reported fallback events, timing and usage are
  recorded separately from configured intent, without copying transcript text
  into accounting. Native frame fixtures cover completed-message accounting
  and the redaction boundary.

- **Analysis considers the user's agent-working environment, not only their
  project.** The default coordination lens and optional capability-leverage
  lens now connect observed friction to better harness use, `AGENTS.md`, and
  surrounding tooling, including OMP subagent roles where applicable.
  Suggestions require version-applicable evidence, costs, and an observable
  benefit rather than unused-feature checklists; Babel does not apply them.
  The cookbook versions are bumped and the embedded cookbook check passes.

- **`babel analysis migrate [--check] [--json]` converges stored analysis and
  title launches without selecting profiles or calling a model.** It removes
  only a trailing legacy `babel` mode argument, preserving custom account
  wrappers, other arguments, exact profile revisions and unknown settings.
  Before an atomic settings replacement it resolves every configured reference
  through the stored worker's offline `engine --describe`; any failure leaves
  settings untouched. A pending `--check` exits 1 without launching a worker;
  canonical checks resolve references offline. Unconfigured machines create
  nothing. Import legacy profiles with Code's `engine --import-profiles` before
  migrating launches when moving to its generic profile store.

- **Babel drives Code's engine over OMP's native RPC and owns everything the
  model is told and everything it submits (#182, code#123).** The
  `babel.analysis-worker` protocol is gone. `babel explore` launches
  `code engine --profile ID@REV --runtime-info PATH`, reads Code's
  `code.runtime/1` sidecar — profile, privacy, cost, containment — and refuses
  the launch before a byte of the prompt is written when the sandbox falls
  short; then it negotiates RPC v2, registers the evidence tools the grant
  covers and one `babel_submit_result` tool whose parameters are the stage's
  result schema, and writes a prompt it composed itself from the stage
  instructions, the recipes verbatim, the sources, the brief and the
  refine-first context. The schema is generated at start-up from
  `explore.Result` and the frontier payload types it embeds, pruned per stage
  by the authority table, so a payload field added on Babel's side reaches the
  model on the next run with no Code edit; the engine validates every
  submission against it before Babel is asked, and Babel's own check —
  references, recipe provenance, and every citation against the corpus hits
  and research documents this run served — answers the model as a tool error
  it can correct, never replacing an earlier accepted submission. Stage
  authority and brief-identifier resolution remain per-item persistence checks.
  Receipts record the registered tools, every call with its decision, the engine's own session
  accounting, and Code's measurements from its finished report. The
  configuration ceremony runs `code engine --configure` and stores what
  `--describe` reports; session titles are an ordinary engine job; `babel
  conformance CODE` grades `--describe` offline and launches one nonce-schema
  job only under `--allow-inference --profile`, after printing the cost. A
  stored `--worker-arg babel` is refused with guidance to migrate explicitly.
  Standard configuration paths, Code's profile/executable overrides and the
  user-session transport survive launch; provider credentials do not. On
  dev-01, a real Babel → contained Code → OMP 18.1.11 round trip against a
  scripted localhost provider persisted a hypothesis, an evidence-backed
  observation, a finding and a proposal across discovery and synthesis,
  without live inference or fleet writes.
  The fixture behind the suites is `internal/worker/testdata/fakeengine`, a
  synthetic `code engine`; `TestWellBehavedRunProducesAReceipt`,
  `TestLaunchIsRefusedBeforeThePromptWhenCodeFallsShort`,
  `TestSubmissionRulesHold`, `TestChunkedFramesAreReassembled`,
  `TestForgedCitationIsRefusedAtSubmissionAndTheCorrectionPersists` and
  `TestSynthesizerConsolidatesServedObservationsUnderItsContract` pin it.

- **The GUI is where a capability lives.** SPEC.md gains §8.4, the shape the
  operator's direction has been asking of Babel for a while: storage is the
  product, everything Babel knows lives in the catalog, the objects it
  references and the ledger, and a run is a stateless worker over that
  storage — it reads what is stored, writes back what it concluded, and holds
  no authority the storage does not already hold, which is why a worker can be
  run anywhere, restarted or replaced. Every stored thing is reachable from
  the GUI's navigation and actionable where it is read, and a capability that
  exists only as a command the operator has to remember is unfinished; the CLI
  stays for automation, for configuring a machine before any UI exists, for
  recovery and for diagnosis, and §8.1 stops calling it a second home for
  operational depth. That is §9.1's rule on the other surface: publication
  must not depend on the operator's memory, and neither may interaction.
  What is missing is named rather than implied — `internal/web/reality.go`'s
  own package doc admits no route asserts a fact or installs a focus rule —
  and starting runs from the GUI is deferred to the manifold migration on a
  dependency, not a preference: the machine channel has no exec verb
  (atyrode/manifold#156), so compute is launched on a machine by hand until it
  does. §14 gains the matching gate and §13 decision 88 records the direction.

### Fixed

- **Records from a run nothing will ever finish now publish themselves
  (#152).** Publication is automatic by design — an hourly push carries this
  host's staged output as its last step — but the drain was keyed on a run
  declaring its own closure, and a process killed hard declares nothing: on
  2026-09-11 two runs left 1,022 records (951 link, 41 observation, 30
  hypothesis) staged on dev-01 with no receipt, no lease and no preparation
  to recover them from, where they sat for five days while `babel sync`
  reported them as belonging to runs "that have not finished". The
  2026-09-06 fix below — *An exploration now declares its publication closure
  when it ends* — closed only the case where a receipt survived to declare
  from; this closes the case where nothing survived but the records. The
  journal (schema v4) enumerates the debt from the staged records
  themselves, which is the only evidence a badly-killed process leaves
  behind; `run.Store.RunLiveness` proves a run over, and names which
  evidence was missing when it does — a lease whose process still exists or
  a receipt standing at running or resumed is live, and nothing else is;
  `sync.Publisher.SealAbandoned` seals the closure of every run proven over
  at what it reached, recording the cause in `sync_run.abandoned_reason`.
  `Retry` seals before it publishes, so every path that already publishes
  without being asked drains the strays, and no new command exists to
  forget. An instance that can prove nothing seals nothing, because
  migration `0003` never lets a declared `record_count` move and a run
  sealed while it could still grow would be permanently short of its own
  output. With only the seal step removed from `Retry`, the regression fails
  with `report sealed 0 runs, want the one that was abandoned`. SPEC.md §9.1
  now states the invariant that was assumed: every record reaches the shared
  catalog with no operator action.

- **One stray disposal handle no longer fails a whole run.** A result that
  deferred or rejected a candidate handle it never declared was recorded with
  `state.fail`, and the first failure anywhere becomes the run's verdict, so a
  three-stage run whose challenger closed clean and whose synthesizer had
  already written a durable finding still reported that the worker could not
  run the exploration. It is a recorded warning now, on the precedent an
  unwritable reference edge already set: the candidate the note was about does
  not exist, so nothing durable is contradicted and only the note is lost.

- **A session Babel scoped is one it can cite.** `babel prepare` fixed a scope
  without registering its sessions in the local catalog, while the reference
  graph checks session endpoints against exactly that catalog — and minting an
  endpoint is a pure digest that always succeeds. Every evidence edge drawn
  from a session reachable only through `--roots` was therefore refused by the
  same resolver that minted it, silently costing those observations their
  navigable provenance. Sessions restored from another host's snapshot live
  outside every adapter default root, so the fleet's corpus lost all of it.
  The catalog refresh runs with an empty scope, which is its no-deletion mode:
  a preparation looked at the sessions it selected, not at every session of
  their harnesses.

- **Receipts written before the native-engine cutover are readable again, and
  the conductor with them.** The cutover reshaped a receipt body's worker half
  without moving `ReceiptSchema`, so strict decoding rejected every receipt
  those runs recorded — and because the budget ledger walks the newest
  receipts before checking any date, one such row failed `babel conductor
  status` and every fresh `babel conductor run` cycle outright. Schema 1 now
  names the pre-cutover shape and schema 2 today's; the retired
  `ProtocolVersion` and `ResolvedCapabilities` are read and deliberately not
  carried forward, `UnknownFields` carries forward as `UnknownFrames`, and
  decoding stays strict so an altered row is still refused. Verified against
  dev-01's own store: 202 of 202 stored receipts decode, and `conductor
  status` reports its 26 recorded cycles, spend and ladder again.

- **`babel fleet records` opens the citation edges it committed.** The reader
  handed every shared-catalog `link` record to the frontier decoder, whose
  narrower vocabulary has no `inspired_by`, so 92 of dev-01's 100 committed
  records listed as unopened errors. Opening now routes on the record's own
  discriminator: `internal/reference` owns the read half of the shape it
  publishes, frontier's typed links keep their path, and a `link` in neither
  vocabulary still surfaces as unopened with a reason. The CLI and web
  listings render the edge's own line. No catalog mutation was needed — the
  records were always valid; the live listing now reports 100 records, none
  unopened.

- **`babel sync --restage` recovers locally durable records that predate the
  publication journal or payload ring (#170).** Recovery uses the owning
  stores' canonical records, including amendments and disposition history,
  without treating ordinary sync as an implicit migration. Isolated recovery
  checks cover repeated restaging and records already present in the journal.

- **Fleet opening preserves the authenticated canonical record payload (#174).**
  Legacy preparation decoding uses its record kind rather than reconstructing
  a lossy substitute, so another host can inspect the original content.
  Round-trip coverage includes legacy preparations and receipt revisions.

- **A configured shared backend with missing payload keys is no longer
  presented as intentional local mode (#167).** CLI and web consumers receive
  an actionable custody failure; storage diagnostics distinguish absent,
  dangling, non-file and present payload-key paths. Isolated placement and
  fleet-opening checks defend the distinction.

- **The provisioning runbook follows the current clan-owned custody handoff
  (#165).** Retired vault-era procedures are no longer presented as current
  setup; unexecuted generation, placement and recovery procedures are explicitly
  operator steps rather than claims of live verification.

- **`babel sync` commits a closure's records sixteen at a time (#180).** Each
  record is four network round-trips — a presence check, the sealed object's
  write, its read-back, the row — and they ran one after another, so a
  700-record run took four minutes to publish and a day's backlog hours.
  Records within a closure are independent (the row insert already tolerates
  two instances committing the same one), so they are now committed
  concurrently; the closure-level verdict is unchanged and a partial closure
  is the same visibly pending state it was.
  `TestConcurrentCommitKeepsTheInvariantUnderAFailure` holds the invariant
  under an injected failure; the serial protocol remains selectable and its
  tests select it.

- **A challenge or synthesis pass no longer fails outright when the model
  attaches observations to its candidates (#171).** With code v0.18.0 every
  challenge and synthesis pass on 2026-09-06 ended with `the challenge stage
  cannot develop observations, and candidate "c1" arrived with 2` for every
  candidate — a full run spent, nothing but refusals recorded, and no finding
  possible. The stage-authority table still persists none of those
  observations; it now drops them with one recorded warning per candidate and
  keeps the candidate and the objection or consolidation beside it, which is
  the material the stage exists to produce. Remedies and findings a stage has
  no authority for are refused as before: an observation is an additive claim
  the table declines to keep, a remedy or a finding is a prescription.
  `TestAChallengerCandidateWithObservationsKeepsTheCandidate` pins it.

- **An exploration now declares its publication closure when it ends, so
  its hypotheses, observations and receipt actually reach the fleet.** Since
  the writers took a staging hook (#138), the one call that ended a run for
  the fleet — `CommitInline` on the hook — declared nothing on the staging
  half, and no exploration since had declared its closure: on 2026-09-06 a
  machine held 50 finished runs and 19,000 staged records that `babel sync`
  reported as belonging to runs "that have not finished", while only
  preparations ever published. The run store gains `DeclareClosure`, which
  internal/explore calls once the receipt is written, and `babel sync`
  declares the closure of every run whose receipt is written and still
  pending before it publishes — the backfill for runs an earlier build ended
  without one. The first sync after the fix published dev-01's backlog.

- **A record the model produced is no longer lost when several explorations
  record into one durable file at once (#173).** Every store began its
  transactions deferred; in WAL mode a transaction that had read and then wrote
  after another connection committed got `SQLITE_BUSY` at once, without the
  busy handler being consulted, and the run logged `persist observation …
  database is locked` while the observation vanished — hundreds of times on a
  machine running eight investigations. `internal/durable` now opens every
  store with immediate transactions, so a writer waits behind a writer instead
  of failing on its first insert, and a sixty-second busy window sized for
  concurrent runs. `TestDeferredTransactionLosesTheWrite` reproduces the loss
  on a plain handle; `TestWriterWaitsBehindAnotherWriter` proves the fix.

- **Babel reads its configuration from `~/.config/babel` on macOS too.**
  `storage.json` and the payload key ring were resolved through
  `os.UserConfigDir`, which on darwin answers `~/Library/Application Support`
  and ignores `XDG_CONFIG_HOME`, while Babel's data and cache directories are
  XDG on every platform and the fleet provisions both documents under
  `~/.config/babel` everywhere. A Mac with both documents in place therefore
  reported `mode local` and refused every fleet read. `config.Dir` now resolves
  `$XDG_CONFIG_HOME/babel`, else `~/.config/babel`, on every platform;
  `TestConfigDirIsXDGOnEveryPlatform` pins it.

## [0.2.2] - 2026-09-06

Shared-mode staging, the manifold plugins, and `babel web` on macOS.
### Added

- **Babel's first manifold plugins, `atyrode.babel` and `atyrode.babel.sessions`
  (operator direction 2026-09-05; `docs/manifold-transition.md` §7).** `plugins/`
  holds two isolated plugins authored against manifold's plugin kit and packed
  by `plugins/pack.sh` into hashed `<id>.manifold-plugin.json` artifacts. The
  baseline owns two doors: `atyrode.babel.run` records that one of babel's
  read-only reports (`archive status`, `archive fleet`, `storage status`,
  `version`) runs on an online enrolled machine, keeps the last fifty records
  and emits `run_recorded`; `atyrode.babel.listRuns` reads them. The sub-plugin
  is one panel over those doors that opens a terminal tile on the chosen
  machine running the report under `sh -c`, held open so the output stays
  readable. Session browsing is not in it: `babel web` binds loopback behind a
  one-time nonce, so the browser waits on a hub-reachable API (#161);
  `atyrode.babel.configure` is reserved (#162). A workflow typechecks, tests and
  packs the bundles on every change under `plugins/`.

- **The manifold plugins have a dev loop, a verifying CI and a delivery path
  (atyrode/manifold#319).** `plugins/` gains `bun run verify`, which installs
  every bundle on a real manifold server spawned from the sibling checkout and
  dispatches every door it publishes, and `bun run dev`, which packs and
  replace-installs on a hub and reinstalls on edit; both are the kit's own
  commands, so nothing is duplicated here. `manifold-plugins.yml` is one call
  of manifold's reusable `plugins.yml` (its ref and `plugins/MANIFOLD_REV` are
  bumped together), and `release.yml` now attaches the bundles and their sums to
  the GitHub Release and hands each one, baseline first, to the integrated
  preview's receiver, so `https://preview.manifold.tyrode.dev` installs a tag
  by itself. Production stays a hand install from the release URL. `AGENTS.md`
  is new and says where a change is seen and what to tell the operator.

- **The cookbook records delivery pipelines as a standing emphasis (operator
  direction 2026-09-02).** The statement gains a "Standing emphases" section
  (version 2) naming continually improved CI/CD in the friction frame: a check
  the pipeline performs is one no agent has to remember, no reviewer has to
  redo, and no operator has to re-explain. The reusable-practice lens (version
  2) includes the pipeline as a capability in its own right — a check performed
  by hand that CI could perform, a pipeline that does not run where the work
  lands, a gate that exists only as a command contributors are asked to run, a
  release step done by hand. The version record is regenerated; `babel cookbook
  check` is clean.

### Fixed

- **`babel web` no longer crashes with SIGBUS on macOS when two requests open a
  fresh session catalog at once.** The first browser load fires two overview
  requests; each ran the coordinator's cold-catalog read, so two connections
  initialised one empty `catalog.db` in the same process. One lost the lock,
  `catalog.Open` mistook `SQLITE_BUSY` for corruption and removed the database
  — WAL index included — under the other connection's memory mapping, which
  Linux tolerates and macOS answers with a bus error. The coordinator now
  serialises catalog reads, `Open` rebuilds only on SQLite's own corruption
  verdicts or an unrecognised schema and returns every other failure with the
  file intact, and `busy_timeout` is set before the first statement that can
  take a lock. `TestOpenLeavesABusyCatalogAlone` holds a write lock from a
  second connection while `Open` runs and proves the writer's table survives;
  before the fix its commit failed with a disk I/O error because the file had
  been unlinked beneath it.

- **A shared-mode deployment stages the records it writes, so `babel sync` has
  something to publish (issue #137).** Every record store shipped a `WithSync`
  option, `explore.Config` a `Sync` field, and `*sync.Publisher` the hook that
  fills them — the whole chain covered by package tests, and not one production
  caller attached any of it. The first payload-key ceremony found the
  consequence on 2026-09-01: `babel tell` on a fully configured host wrote a
  durable complaint, `babel sync` answered `published 0 records in 0 runs; 0
  still pending`, and `babel fleet records` showed nothing. Runbook §9.1's
  `local` — "the record was never staged" — was the permanent state of every
  real deployment, and every record it held was owed to the fleet by nobody.
  - **Writers stage; `babel sync` publishes.** internal/sync's two halves are
    now separately attachable, which is what makes the wiring possible at all:
    staging is transaction-local, so a `sync.Stager` holds no handle of its own
    and every store internal/cli opens is handed one in shared mode. The full
    `*sync.Publisher` — the half that holds the catalog, the object store and
    the payload keyring — stays exactly where it was, in `babel sync` and the
    reconcile step after an archive push. So `babel tell` dials nothing: it
    records what it owes inside the transaction that makes the complaint
    durable, and the journal is the handoff. That ordering is SPEC.md §6.5's,
    and it is the reason a publication outage still cannot fail a local write.
  - **A deployment with no payload keys stages anyway.** The gate on the hook
    is storage mode and nothing else, split out of `syncUnavailable` so that
    staging and publishing keep one answer where they agree: a shared host that
    cannot yet seal a record must still record that it owes one, because
    SPEC.md §9 requires staged output to be visibly pending rather than
    quietly local.
  - **Every store internal/cli opens gets the hook, with no read-only
    exemption.** A hook is attached for the life of the handle, so "this call
    site only reads today" is a judgement the next caller inherits without
    knowing it was made — which is how the frontier and the complaints
    `babel prepare` reconciles came to be opened without one. `babel explore`'s
    config carries the same hook the state's stores were opened with, so a
    run's records and the receipt that ends it publish under one closure.
  - **The surfaces that report staging can see it.** `babel web`'s record pages
    and every local listing's SYNC column now resolve against this machine's
    publication journal in shared mode. Before, an empty journal made `local`
    true on every deployment; with writers staging it would have been the one
    lie runbook §9.1 says the visible-staging requirement must not tell.
  - **Local mode is unchanged, down to the table list.** With no shared
    configuration the hook is a nil interface, each `WithSync` is exactly the
    option nobody passed before, and a local-mode `babel tell` still creates no
    journal tables at all — which a new test asserts, beside a shared-mode
    drill that tells with no keys placed, checks the journal, generates a key,
    syncs, and reads the record back out of PostgreSQL as committed. A
    source-reading guard fails the suite if any store in internal/cli is opened
    without the hook again, or any `explore.Config` built without one, because
    a detached hook compiles, runs, writes durable records and publishes
    nothing, silently, which is the failure this entry describes.
- **An exploration on a machine configured before #86 no longer dies as
  "exited without a result".** The stored worker arguments on such a machine
  carry `--set`, which Code refuses on its own terms — a dial is turned in the
  configuration ceremony and nowhere else. The ceremony and the titler already
  refused a stored dial; exploration did not, so it launched the worker, and
  the run's verdict named neither the cause nor the remedy. It is now refused
  before launch, by the check that already existed, at the one call site that
  lacked it. Measured on the workstation: 146 seconds to the opaque message,
  immediate to the named one.
- **A requested stop no longer exits 1 because a browser opened a connection
  it never used (issue #130).** Chrome's speculative preconnects are accepted
  and silent, `http.Server.Shutdown` deliberately never closes a connection
  that has not delivered a request (golang/go#22682), and the drain deadline
  is shorter than the header timeout that would eventually reap one — so a
  preconnect opened within five seconds of the lock held the drain to its
  deadline and turned "server stopped as told" into a failure, five CI hits
  in one day. `babel web` now closes connections that never sent a request
  the moment shutdown begins; genuine drain failures still report as errors,
  and a regression test plants the silent connection and bounds the drain.
- **`babel conformance` said nothing at all while it graded (issue #78).** The
  2026-08-30 readiness drill pointed the suite at `/bin/cat` and watched two
  minutes forty-five seconds of empty stdout, then all eleven result lines at
  once. Obligations are graded one at a time — eighteen of them now — and one
  that cannot reach the worker spends its whole 15-second handshake budget
  before its verdict exists, so the report was collected and printed after the
  last of them. The command's own help promised "one obligation per line",
  which reads as streaming, and an operator watching an empty terminal had no
  way to tell a slow grader from a hung one or to see which obligation was
  stuck. Each verdict is now written the moment that obligation settles: the
  last line on the terminal names the last thing decided and, by omission, what
  is being graded now. Measured against `/bin/cat`, the same 50 seconds that
  produced no bytes before now produces three named failures, at 15, 30 and 45
  seconds. Nothing about any obligation's semantics changed — the same
  assertions decide the same verdicts in the same order — and the 15-second
  budget is still the budget, which the help text now states, because a line
  that takes 15 seconds to appear is worth predicting.
  - **`--json` is exempt, and unchanged.** A machine-readable report is one
    parseable document, so a `--json` invocation subscribes to no stream and
    emits the same document, with the same fields, after the last obligation.
  - `worker.RunConformanceWith` still returns the whole report; the streaming
    caller uses `worker.StreamConformance`, which delivers every verdict —
    including the ones reported unrun after a cancelled context — on the
    calling goroutine, in obligation order, and returns the identical report.

### Added

- **The acceptance drill's repository half addresses a real object store
  (issue #20).** The §14 two-instance and outage gates could be pointed at an
  operator's PostgreSQL but always archived into a local path, so "against real
  Cellar" was the one bullet the 2026-08-31 run explicitly did not claim.
  `BABEL_ACCEPT_REPO` is now `BABEL_ACCEPT_PG_URI`'s counterpart — a base restic
  locator, with `BABEL_ACCEPT_REPO_KEY_ID` and `BABEL_ACCEPT_REPO_KEY_SECRET`
  required together and named rather than quoted when one is missing. Each
  scenario archives into its own prefix under that base, so runs never collide,
  and each purges its prefix and re-lists to confirm the bucket was left as it
  was found. Nothing below the selection branches on local versus real, so the
  drill cannot prove less against the store than against a directory. Measured
  against Clever Cloud Cellar on 2026-09-01: eleven scenarios on the store, ten
  pass and one skips for a catalog-side reason, 402.5 s, bucket empty
  afterwards.
  - **`TestArchiveRestoresWithResticAlone` runs against the object store too.**
    The direct-recovery guarantee is now exercised where recovery would actually
    happen: the restic binary, the repository password file, and the two `AWS_`
    variables that are the only form restic accepts a store credential in — no
    storage document, no catalog, no host identity. Every fixture file came back
    byte-identical out of Cellar.
  - **A repository outage is expressed in the store's own terms.** An object
    store has no directory to rename, so the gate that moves the repository out
    from under a running push moves every object to a sibling prefix
    server-side and deletes it from this one, then moves it back. The push
    fails naming the repository, the catalog gains nothing, and the next push is
    ordinary — the same three assertions the local path makes, with no invented
    outage semantics and no skip.
- **The cookbook opens with a statement of what it is for (issue #120).**
  `cookbook/preamble.md` carries SPEC.md §1's charter into the asset tree:
  Babel's axiomatic center is the friction between an operator and their
  agents, and the cookbook's own evolution is measured against it. It is
  explicit that this weights and curates rather than narrows — the serendipity
  floor, effective patterns, and open discovery stay chartered — and it states
  what a recipe diff must answer: what friction it makes visible, for whom,
  and why an existing lens does not already reveal it. The statement is
  versioned and digested under §5.1's rule like a recipe, recorded in
  `versions.json` (now `manifest_schema: 2`), reported by `babel cookbook
  check`, and named by `babel cookbook list`.
- **The conductor's duty rung draws friction lenses first (issue #120).** A
  standing duty now declares whether its subject is operator-agent friction,
  and #94's `mechanization-audit` — where inference substituted for retrieval
  because tooling or context was missing — leads the product dimension instead
  of trailing it. The ladder is unchanged: duties still wait behind the
  operator's invitations, and the serendipity floor's protected fraction is
  untouched, so friction-primacy cannot become friction-exclusivity.
- **`babel archive unlock` clears the stale restic locks an interrupted
  command leaves behind (issue #108),** with Babel's own repository,
  password-file, and object-store plumbing. It lists every lock with the
  staleness judgement it reached and the reason for it before removing
  anything, removes only locks that are both stale and shared, and removes a
  held or exclusive lock only when the invocation names its id. A run that
  removes nothing exits 0 and says so, and no timer, conductor duty, or other
  autonomous path can invoke it.
  - The restic port grew three lock verbs — `list locks`, `cat lock`,
    `unlock` — and the invariant behind it sharpened: no verb can delete or
    rewrite archived data, and the one verb that removes anything reaches only
    restic's own lock files. `forget`, `prune`, and `repair` stay outside
    Babel.
  - `docs/runbook.md` §2.4's OPERATOR STEP is one command instead of a
    hand-assembled restic environment, and it records what the drill got wrong
    about restic: plain `restic unlock` removes a stale exclusive lock too, so
    `--remove-all` is what a lock that is *not* stale needs.
- **Proposals are first-class records, joined to the hypotheses they answer by
  `addresses` edges (issue #114, operator direction 2026-08-31: new outputs
  only).** A candidate that carries a suggested change now emits two records
  instead of one: the claim about how things are, and the remedy for it. Each
  has its own revision chain and its own dispositions, so accepting a claim as
  true while rejecting its remedy as unwanted — or the reverse — is a decision
  the records can actually hold. No existing hypothesis was migrated.
  - A proposal states which of its two forms it has: a consolidated proposal
    rests on findings that rest on evidence; a candidate proposal rests only on
    the claim it addresses and is shown as a want or an option rather than as a
    verified fact. The form travels fleet-wide, including to hosts that cannot
    decrypt a word of it.
  - The five default cookbook lenses are version 3, telling a worker to emit
    claims and remedies as separate addressable records, to prefer a claim
    alone when the record does not support a remedy, and to offer competing
    remedies separately rather than one hedged suggestion.
  - A hypothesis page lists the remedies addressing it. `babel hypothesis show`
    gains a "remedies addressing this claim" section, `babel finding show` a
    FORM column, the web record page `proposals_addressing`, and the review
    export introduces a candidate remedy as a want rather than as Babel's
    canonical review artifact.
- **`babel tell` — complaints as operator-authored steering records (issue
  #115).** `babel tell TEXT` records something the operator wants Babel to know
  that Babel did not ask about: a complaint, a wish, or a hunch. The text is an
  argument or the whole of stdin, `--about` names an analysis record it is
  about, `--amend` restates one already told, and `--json` emits the record
  with its adjacency. Capture prints what Babel already holds touching the same
  words, so telling it something is also asking what it already has.
  - A complaint is a first-class Phase B record: durable, envelope-encrypted,
    pending-sync, insert-only, with its own revision chain and its own place in
    the reference graph. It enters the retrieval index beside Babel's own
    output, so every later preparation surfaces the operator's standing
    annoyances as context rather than only Babel's prior conclusions.
  - A complaint has no lifecycle. Nothing is opened, assigned, scheduled, or
    closed, and there is no resolved state to leave stale — "was this
    addressed?" is a citation query on the complaint's own page, answered by
    the records that carry an `addresses` edge to it. A complaint may be
    amended and never ended: `--amend` appends a new wording and keeps the
    earlier one, because the operator may say it better later and the earlier
    wording is evidence of what they thought at the time.
  - `babel fleet records --kind complaint` lists them across the fleet, and
    `migrations/0011` admits the kind to the shared catalog.
- **The complaint capture box, and complaint record pages (issue #115).**
  `babel tell` gave the operator somewhere to say what is going badly; the
  review surface now has the same thing behind a text box, and what was said
  has a page. The box rides `/review` by operator decision (2026-08-31) rather
  than becoming a seventh destination: the operator who came to review is the
  operator with something to say.
  - Capture is an attributed operator decision on §4.7's terms: `POST
    /api/complaint/tell` resolves the author and host exactly as a disposition
    does and refuses rather than defaulting when either is unnameable. The §14
    whitelist gains `ComplaintService` and nothing else — `Amend` is absent
    because no record page on this surface edits a record's wording.
  - The capture answers with what Babel already has touching it: the spend-free
    FTS pass #115 asks for, capped at a screenful, carrying no score, reading
    the partition as the last reconcile left it. Every failure is a note and
    none fails the capture, because the complaint is durable before the pass.
  - A complaint's page is its words, its chain, and what cites it. The body
    renders verbatim inside the quoted frame untrusted prose already uses; any
    wording opens at its own id and reports the chain's head, so a citation of
    a superseded wording still leads to what the citing record saw.
  - Nothing here closes, and that is asserted rather than promised: one test
    walks every response's JSON at every depth for the keys that would make
    Babel a work tracker, and a browser test walks the rendered controls for
    the same thing.
- **Three draft lenses: where the memory, the tests, and the time actually go
  (operator direction 2026-08-31).** The cookbook gains `document-ceremony`,
  `test-economics`, and `time-and-spend`, each shipped `default: false` under
  §5.5's draft discipline — runnable by name, promotable once corpus
  evaluation sharpens their overlap with the five default lenses.
  - `document-ceremony` follows durable memory through its whole observable
    lifecycle — promise, write, fetch, abidance, update, rot — and prices
    ceremony against the fetches that actually happened. Its costliest class
    is `stale-followed`: a document that was trusted and wrong.
  - `test-economics` reads tests as purchases: what an assertion defends,
    which failures redirected source and which only ever edited expected
    values, what suites measurably cost against their recorded payouts. The
    hindsight-prosecution guard is explicit: after any bug, "a test was
    missing" is always true and never useful.
  - `time-and-spend` decomposes recorded wall time and token spend into
    named, measured sinks — recurring waits, context reconstruction, retry
    loops, serialized independence — with the hard boundary that thinking is
    never priced, and every rate carries its window and arithmetic. It leans
    on the usage-metadata join (#89/#92) for the token half.
- **Every Babel record can now cite every other, and the citation is a record
  (operator direction 2026-08-31, issue #113).** Babel had three ad-hoc ways to
  say that one thing rests on another: an evidence locator inside an
  observation, a revision chain's `supersedes` column (#87), and a
  preparation's free-text "related" refs. None of them could be queried
  together, none carried provenance, and the free-text one could name a record
  that does not exist. They are replaced by one model: typed, append-only edge
  records with an actor, validated at write time, published to the fleet.
  - **`internal/reference`, the graph.** The frozen contract - six relation
    kinds (`evidence`, `supersedes`, `refines`, `addresses`, `inspired_by`,
    `duplicates`), a `RecordRef` of namespace plus durable id, and the
    `Appender`/`Lister` halves - is now backed by a durable store in the same
    `durable.db` every Phase B component shares, under the component key
    `reference`. An edge is immutable and never deleted, enforced by triggers
    rather than by the package's own SQL: a wrong link is answered by a later
    edge, which is §4.7's rule that rejection never deletes, applied to
    citations. A UNIQUE over the relation and both endpoints makes a re-append
    return the edge that already exists - the same id, the same timestamp -
    because emitters retry and two rows asserting one citation would make "how
    many times was this asserted" a question the graph answers wrongly.
  - **A hallucinated target is a write error.** Every endpoint is resolved
    against the store that owns its namespace before anything is staged:
    hypotheses, observations, findings and proposals against the frontier, runs,
    receipts and preparations against the run store, dispositions and Reality
    facts and entities against theirs, sessions against the local session
    catalog. This is the draft-issue anchoring rule turned on the corpus
    itself, and it fails closed - an unregistered namespace is refused rather
    than admitted unvalidated, so a store nobody opened cannot be cited, and a
    namespace #113 anticipates but Babel has not built (a complaint, #114/#115)
    is refused by name rather than wired to a resolver that always says no. A
    resolver's own failure is reported as itself: an unreadable durable file
    must not look like a fabricated citation.
  - **A session is addressed by its durable session key, never its selector.**
    The key is the `sessions.session_uid` digest over deployment, host, harness
    and adapter source id. That is not a preference: an edge's endpoints
    publish as plaintext PostgreSQL columns, and a selector embeds the source
    id, which embeds a workspace-derived project slug that Babel deliberately
    keeps out of the shared catalog. A selector endpoint therefore fails
    resolution instead of leaking a path, and the same derivation an emission
    site uses to mint a session endpoint is the one that resolves it.
  - **Edges publish as Phase B records, shape in the clear and note sealed
    (SPEC.md §763).** `migrations/0008_reference_edges` adds `analysis_edges`:
    the edge's record id, its relation kind in a closed CHECK, and both
    endpoints as namespace-plus-id. Nothing else. The note - the only prose an
    edge carries - travels in the envelope-encrypted object like every other
    Phase B payload, and there is no column for it. So the fleet-wide graph of
    what cites what is navigable on a host holding only the catalog credential
    and no payload key (#112): a sealed record still shows where it sits in the
    web of citations, and says nothing about what it claims. The addition is
    argued in the migration and enforced by the existing gate - the columns are
    allowlisted, the table is a Phase B table, and `AssertPhaseBPlaintext`
    refuses a title-, path-, grade- or money-shaped column on it. `SchemaVersion`
    stays 1: additive, and `EnsureCompatible` already refuses a database
    migrated past the binary.
  - **The endpoints survive a crash, because they are staged rather than
    re-derived.** An edge stages inside the transaction that makes it durable,
    with its plaintext endpoints beside its sealed payload (journal schema
    version 2, upgraded in place), and publishes immediately afterwards. Killed
    between the commit and the publication, `babel sync` converges: exactly one
    record row and exactly one citation row, whatever the retry count, since
    nothing could reconstruct those columns from a sealed object. The two rows
    are written in one PostgreSQL transaction for the same reason - a sync
    never revisits a record the catalog already holds, so a crash between them
    would leave a citation invisible forever. An edge a run asserted joins that
    run's closure and publishes when the run ends; an operator's edge is its own
    closure of one, declared in the writer's transaction, because nobody
    resumes an operator's act.
  - **Emission is by injection, and absence degrades.** Revision minting,
    evidence absorption and preparation injection receive `reference.Appender`;
    the CLI and web surfaces receive `reference.Lister`. Nil injection is a
    supported deployment: reads return no links and the surface renders a record
    with no citation section rather than an error, and a write path that forgot
    its nil check reports a condition instead of taking the process down.
  - **Both surfaces render outgoing links and backlinks.** They are the same
    table read from two sides rather than a backlink index that can fall out of
    step with the edges, bounded by default so a heavily cited record cannot
    arrive whole, and rendered through the existing sanitizer - a note is
    untrusted text, and the hostile-content rules are unchanged.

- **Phase B records now reach the shared catalog (operator direction
  2026-08-31, issue #109 items 1-2).** SPEC.md §6.5 and §9 have always promised
  globally durable, object-first/PostgreSQL-last Phase B records with visibly
  `pending-sync` outage staging, and every durable record has been born
  `pending-sync` since it was first written — but the publisher that flips them
  to `committed` was never built. So tonight's hypotheses were bound to the
  workstation that produced them, and `durable.db` is deliberately not under the
  hourly archive roots: a dead disk lost analysis outputs that exist nowhere
  else. It no longer does.
  - **`internal/sync`, the publisher.** A local journal in the same
    `durable.db` every Phase B component shares records what has been staged
    and what has reached the fleet, which is the only thing that can answer
    "what does this machine owe" while PostgreSQL is unreachable. Publication
    is `internal/sharedcatalog`'s existing commit protocol: declare the run,
    seal each missing record's payload into the object store and read it back
    before any row names it, then flip the run — the visibility boundary —
    conditional on the catalog holding the whole declared closure. The local
    flip happens strictly after that transaction commits, so the two crash
    windows behave differently and both converge: killed between the object
    write and the row, the retry writes a new content-addressed object beside
    the orphan and inserts exactly one row; killed after the commit and before
    the flip, the retry writes no object, inserts no row, and flips only.
  - **Staging shares the writer's transaction, and that is the whole design.**
    A record that committed locally while its journal row did not would be
    durable, invisible to the publisher, and reported by nothing. So
    `internal/frontier`, `internal/run`, `internal/disposition` and
    `internal/reality` stage inside the transaction that makes the record
    durable, and attempt publication immediately after it commits. A writer
    with no publication hook — local mode, the default — behaves exactly as
    before and stages nothing.
  - **A run publishes as one closure.** `migrations/0003` fixes a run's
    `record_count` at declaration and never lets it move, which is what makes
    "a partial commit is not a commit" a database property. So a record a run
    produced joins that run's closure and publishes nothing yet, and
    `internal/explore` — the only thing that knows when a run has ended —
    declares and publishes it. §5.4's challenger and synthesizer join it too:
    their `<run>/<stage>` identities name jobs within a run, and three closures
    would make one exploration become globally reviewable in three unrelated
    pieces. A record no run produced — an operator's decision, a Reality fact,
    a preparation — is its own closure of one, declared in the writer's own
    transaction because nobody resumes an operator's decision. Reject-and-refine
    publishes as one closure of two, so §4.7's atomicity survives the boundary
    rather than stopping at the local transaction.
  - **`babel sync`, and a reconcile after every push.** It retries every
    pending closure, is idempotent by global entity id, and reports per-kind
    counts plus what is still owed — including records of a run that has not
    finished, which are deliberately unpublishable and never dropped.
    `babel archive push` runs it as a final, non-fatal step after the Phase A
    catalog reconcile, so an hourly timer drains the backlog without a second
    schedule.
  - **Publication is never a write-path dependency.** An unreachable catalog, a
    refused object write, a missing payload key: each leaves the record durable
    and visibly pending, emits one sanitized diagnostic line, and lets the
    command that produced the record succeed. That is not leniency; it is the
    only ordering under which an outage cannot destroy output.
  - **`internal/objectstore`, a Cellar-backed store with no new dependency.**
    Hand-rolled AWS SigV4 over `net/http` for two verbs against one bucket,
    because adding a provider SDK to a public repository whose crypto is meant
    to be audited by reading is the worse trade. The Phase B object namespace is
    derived from the restic locator as a *sibling* prefix — `babel/v1-analysis/`
    beside `babel/v1` — so it lives in the operator's own bucket, provably
    disjoint from restic's tree, and needs no field in the frozen
    `storage.json`.
  - **Payload keys live in their own document**,
    `$XDG_CONFIG_HOME/babel/payload-keys.json` at mode 0600 beside
    `storage.json`, created by `babel sync --generate-key ID`. It is separate
    because `config_schema` 2 is frozen, and because the lifecycles differ: a
    locator is a current value, a key document is a history. Rotation appends
    and promotes; the writer refuses to replace an existing document, because
    doing so would orphan every sealed object written under the keys it held and
    Babel deletes no remote object. See docs/runbook.md §8 for custody.

- **Committed Phase B state is readable from every host (issue #109, items
  3-5).** Analysis was globally durable and locally readable, which is a
  contradiction: `SPEC.md` §520 and §645-646 promise globally browseable
  committed state, and every listing still answered out of one machine's
  `durable.db`. Tonight's hypotheses were bound to the workstation that
  produced them, and two conductors on two machines could explore the same
  candidate without either being able to find out. The read half now exists.
  - **`sharedcatalog.Records`** lists committed Phase B records fleet-wide with
    plaintext metadata and a reference to the sealed object, filtered by host,
    instance, kind, run, record id or commit window. Content is decrypted
    client-side by the caller; there is still no payload column and no API here
    that returns plaintext from PostgreSQL. `RecordHosts` reports the machines
    that have committed something, which is what a host filter offers, and
    `RecordSyncStates` answers per-record sync state from the store that is
    authoritative about it.
  - **Host attribution is read rather than inferred.** `migrations/0007` adds
    `instances.host_id` — the pairing `Register` already received in one call
    and dropped on the floor. Without it a committed record could only be
    attributed by reading `execution_host_id`, which `migrations/0003` defines
    as a rerun pin and leaves NULL for every unpinned run, so the alternative
    was conflating authorship with a rerun constraint or guessing from an
    instance id. An instance that has not registered since the column existed
    renders as `unattributed`, and Babel refuses to substitute a host: a record
    filed under the wrong machine is invisible where a gap is not.
  - **The frontier answers across hosts.** `babel fleet ingest` fetches every
    host's committed records, decrypts them locally and indexes them into the
    rebuildable retrieval cache, per host, so self-retrieval and dedup reach the
    whole deployment. Nothing remote is ever written to a durable store — which
    is what keeps §763's local-only-indexing decision true, and what stops an
    ingested record from being republished forever by the machine that read it.
    A worker's self-retrieval hits now carry the machine each idea came from,
    because telling a conductor "this already exists" while withholding whose
    analysis it is would be inference presented as fact.
  - **`babel fleet records`**, plus a `SYNC` column and a `--fleet` flag on
    `hypotheses`, `findings`, `review queue` and `dispositions`; the web review
    inbox, frontier and receipts render fleet-wide behind a host filter chip
    with pending-sync rows visibly marked. A listing without `--fleet` still
    answers with PostgreSQL down, because the local durable store is local and
    a shared-catalog outage must not cost an operator their own analysis.
  - **Sync state is four values, not three.** `committed`, `pending-sync`,
    `local` and `unknown`, resolved in one place. `local` is deliberately not
    `pending-sync`: pending is a promise that something will carry the record,
    and for a record no journal is holding on a machine in local mode nothing
    will. `unknown` is what a row reports when the authority was unreachable,
    because each of the other three would be a specific false statement.
  - **Plaintext eligibility is settled and enforced (§763's open item).** A
    Phase B plaintext column may belong only to the six classes §9's Phase B
    vocabulary names; `AssertPhaseBPlaintext` is called by `Verify` and
    therefore by every `Migrate`, so a future migration putting a title-shaped,
    path-shaped, grade-shaped or money-shaped column on an analysis table
    passes the general allowlist and fails this gate. The operator's 2026-08-30
    widening was granted for archive metadata about sessions a harness had
    already written, and it does not carry over to claims Babel produced about
    the corpus.
- **Self-improvement duties, behind operator toggles (operator direction
  2026-08-31, issues #88 and #94).** Babel's charter has always included "and
  Babel itself", and until now that meant an operator typing a command. The
  conductor's rung two — declared and unimplemented since #96 — is now the
  duty rung: when the operator has authorized a dimension, the loop schedules
  that dimension's recipes on a slow cadence, and every such cycle records a
  `policy` authority naming the duty it discharged.
  - **`babel conductor configure --babel-improves-babel` and
    `--babel-tunes-itself`, both off, each with an explicit `--no-` form.**
    They are flags rather than #86-style ceremonies, and the reason is what
    they authorize. A ceremony exists where the operator chooses model
    authority through Code's own interface; a duty toggle grants nothing a
    cycle did not already have — same ceremony-minted profile, same ceilings,
    same read-only corpus, same "suggestions, never side effects" boundary.
    What it authorizes is *scheduling*: that a cookbook recipe whose subject is
    Babel itself may be drawn without being asked for each time, which is a
    scheduling decision and belongs in the document that holds the other
    scheduling decisions. The `--no-` forms exist because `configure` is
    incremental: an operator adjusting the floor has not withdrawn a duty, so
    "not named" has to stay distinguishable from "off", and naming both forms
    at once is refused rather than resolved by precedence.
  - **Three duties, two dimensions, one recipe each.**
    `babel-improves-babel` and `mechanization-audit` ride the product toggle
    (#94 places the audit in that dimension); `babel-tunes-itself` rides the
    personal one. A duty is drawn at most once per day, only when the
    operator's own invitation queue is empty, and the serendipity floor is
    still checked first — standing work is exactly the pressure that would
    converge the loop into pure dutifulness, so the protected chaotic share
    binds against it rather than yielding to it. The cadence is computed from
    the journal, so restarting the conductor does not hand its duties a fresh
    day.
  - **Off duties are visibly off.** `conductor status` gained a `duties` block
    listing every duty this build knows with its own state — off with the flag
    that would authorize it, due, or dated with when it comes back. "Babel has
    no such duty" and "you have not authorized it" are different answers to
    "why is the loop not doing this", and a status view that listed only what
    was on would give the first answer for both. Rung two now reports itself
    implemented with a depth, and its note still says what it lacks: the
    spec's attention policy — lifecycle, focus, fleet — is not implemented in
    this build.
  - **Three new cookbook recipes, none default-enabled**, each following §5.1's
    ten-section structure and each running only under its toggle or when named
    explicitly. `babel-improves-babel` evaluates output quality and acceptance
    from the evidence #87 already records — the disposition ledger, the
    append-only revision chains, the frontier statuses — with no new telemetry
    layer, and carries the constraint that makes that safe: acceptance is
    evidence, never a target, because an instrument that optimized its own
    acceptance rate would do it by saying less that is surprising. Its
    proposals reach the public codebase as `draft-issue` dispositions anchored
    to a repository verified from a local checkout's own git configuration.
    `babel-tunes-itself` carries #87's item 6: it revisits authorized Reality
    facts and accepted durable-learning notes against fresh archive state and
    proposes amendments, scope narrowings, disputes and retirements through
    `propose-reality-fact` and `store-memory`, and it treats silence as what it
    is — a stable world produces no evidence, so absence never justifies a
    retirement. `mechanization-audit` reads the receipts, the recorded usage
    and the repeated-search patterns for places where inference substituted for
    retrieval, and proposes the code that would have served the context;
    its metric is mechanization rather than frugality, and it carries #94's
    anti-convergence guardrail as a binding rule in its own wording: efficiency
    pressure applies only to substrate — retrieval, tooling, context
    assembly — never to hypothesis content or diversity, and no recipe may be
    graded on producing cheaper thoughts, only on wasting less budget before
    thinking starts.
  - Each finding is classified into exactly one dimension. A product finding
    stored as this operator's memory is a public proposal buried where nobody
    will read it; an operator-specific amendment published as a draft issue is
    a proposal nobody else can evaluate and a leak of this operator's working
    context. Each recipe states the dispositions it may emit and the ones that
    belong to the other dimension.

- **The job document is staged: containment is declared before the run's
  credentials are sent (issue #71, SPEC.md §14).** Decision 53 and §14 claimed
  Babel "refuses a declaration short of the run's requirement before any job
  material reaches it". The 2026-08-30 audit measured that claim and found it
  false: the complete job — source selectors, capability grant and the
  run-scoped broker token — was written to the worker's stdin immediately after
  the handshake, and the containment check ran when the worker's first event
  arrived. A worker that under-declared executed no analysis, but already held
  the run's credential and knew which sessions had been selected, and nothing
  stopped it from copying its stdin before declaring anything. The exposure was
  bounded, not closed.
  - **Two stages, with the declaration between them.** Babel now writes a
    `job-preamble` carrying the run's identity, the profile to resolve and the
    run's parameters — what a worker needs to resolve itself and to know which
    kind of run this is, and nothing it could read, retrieve or authenticate
    with. The worker answers with its resolved configuration, containment
    declaration included. Only then does Babel write the `job` message: the
    recipes, the grant, the sources and the broker token. A worker whose
    declaration falls short is answered with the handshake's refusal message
    instead, naming the properties that fell short, so it exits rather than
    blocking on a read for material that is not coming.
  - **Measured on the worker's stdin, not on Babel's intentions.** The
    fake-worker fixture records every byte written to it, and the test asserts
    that a refused run's capture holds the preamble and the refusal and neither
    the credential nor a source selector — for a weak declaration, an absent
    one, and one that provides nothing. Two more fixtures cover the ways a
    worker can break the ordering from its own side: one that writes progress
    or a result where its declaration belongs, which is refused there, and one
    that will not declare until it has been given the material, which is the
    version 1 worker and now stalls with the credential unwritten.
  - **Protocol version 2, and no compatibility path.** The ordering is the
    whole of what the declaration buys, so a build that could still accept the
    old ordering would keep the exposure reachable. Version 1 and version 2
    disagree about who writes next, and a mismatched pair is refused in both
    directions with an error naming both version sets — verified by running
    this build against the previous Code and the previous Babel against the new
    one. Both repositories move together; the fleet has one operator and one
    deployment path, so there is nothing to stage a migration for.
  - **The suite went from sixteen obligations to eighteen.**
    `run/declares-from-the-preamble` fails a worker that cannot declare until
    it holds the material, and says so in those words instead of as a timeout,
    because "your worker is waiting for a write that is conditional on the
    event it is not sending" is not something an operator infers from
    "stalled". `run/refused-before-credentials` grades the refusal path through
    a new `under-declare` directive — the second directive that asks a worker
    to misbehave deliberately, because a worker that always declares enough is
    never refused and the path that decides whether the credential travels
    would otherwise be graded by nothing. Both are proven discriminating by
    fixtures that fail them, and `run/forward-compatible-job` now plants an
    unknown field in each stage rather than one in the job, since tolerating
    one message's unknown fields is not tolerating the other's.
- **The conductor: an attributable autonomous runtime (operator direction
  2026-08-31, issue #96).** Babel ran only when summoned. That is a strange
  default for an instrument whose subject matter accumulates whether or not
  anyone is watching, and the operator's objection to the summoned-only model
  was not that it was slow but that it was opaque: what Babel analyses, when,
  and by whose authority. `babel conductor run` is a foreground loop that
  answers "what deserves a run?" once per cycle, and every part of the answer
  is recorded. It adds scheduling and nothing else — a cycle is an ordinary
  preparation, recipe, receipt, frontier and disposition write, reached through
  the same code a typed `babel prepare` and `babel explore` take — so turning
  it off degrades Babel to exactly the manual instrument it was.
  - **Every run says why it happened.** Run receipts gained an `authority`:
    `operator` with the command or invitation that asked for it, `policy` with
    the standing policy that directed it, or `serendipity` with the identity of
    the draw. It lives in the receipt header rather than the body, because it
    is an identifier pair and §9's plaintext allowlist admits one: a deployment
    that seals receipt bodies can still list runs by why they happened. A run
    with no nameable authority is refused before it reaches a worker, which is
    #86's intentionality rule applied to scheduling. Receipts written before
    the field existed carry none and say so; nothing is backfilled, because
    writing "operator" over them would manufacture the provenance the field
    exists to carry. The durable schema migrates in place — the run component
    moves to version 2 — so an existing file opens and keeps its history.
  - **A work ladder, with the operator above the loop.** Rung one is the
    process-further queue of #87: the oldest invitation nobody has taken, run
    over the sessions the invited record came out of, with the recipe that
    produced it. Rung two is the spec's attention policy, and this build
    declares it and does not implement it — `conductor status` prints it as
    absent rather than as empty, because "no policy is waiting" and "this build
    has no policies" are different answers to the question the loop exists to
    keep answerable. The floor is serendipity: a random corpus slice crossed
    with a random default-enabled recipe, no aim, its draw declared as a ULID
    the receipt records.
  - **The serendipity floor is a protected fraction, not a last resort.** One
    cycle in four is chaotic by default even while invitations are queueing,
    configurable with `--floor`. The ratio is computed from the journal rather
    than from one process's memory, so restarting the loop does not hand it
    three free dutiful cycles. Every incentive in a loop like this pushes
    towards pure dutifulness, which is why the share is guaranteed rather than
    left to whatever is waiting.
  - **Autonomy is budget-bounded, not trust-bounded.** `babel conductor
    configure --per-cycle AMOUNT --per-day AMOUNT` is mandatory and has no
    defaults, and the loop refuses to run without both. Before each cycle the
    day's receipted estimated costs are summed — from the receipts, so a run an
    operator started by hand spends the same budget and a restart does not
    forget the day — and a cycle that would not fit parks the loop with a
    reason an operator can read. A cycle that overran the per-cycle ceiling
    parks it too: the first breach is evidence the next one will do the same.
    A run whose profile quoted no usable currency is counted as unpriced rather
    than as free, and `conductor status` shows the count beside the total.
  - **A cycle inherits the stored profile.** The conductor never configures
    one and has no `--profile`: a loop that could choose its own profile could
    choose its own spending limit.
  - **Stopping is clean at cycle granularity.** The first SIGTERM or Ctrl-C
    ends the loop after the cycle in flight finishes and is receipted; a second
    cancels the run itself, which internal/explore already makes safe. A cycle
    is journalled before its run starts, so a killed conductor leaves the fact
    behind — and the next one resumes that cycle under its original run
    identity rather than drawing the same work twice. An invitation is claimed
    before the run, so losing a conductor costs one amended receipt and never a
    duplicate run over an operator's request.
  - **`babel conductor status` is the loop read back.** State, the cycle in
    flight and its authority, every rung's queue depth, today's spend against
    the ceilings, and the last N cycle outcomes — assembled from the journal
    and the durable stores rather than from a second source that could disagree
    with them. A journal entry claiming a cycle is running is checked against
    whether that process still exists, so a crashed loop reads as interrupted
    instead of confidently working. There is no daemon mode: supervision,
    restart policy and wall-clock scheduling belong to the OS.

- **Frontier self-retrieval and refine-first context (operator direction
  2026-08-31, issue #87 item 4).** Babel could search the corpus and could not
  search itself. Nothing in a run had any mechanical way to learn that the
  candidate it was about to mint had already been minted, developed, argued
  with and rejected three runs earlier, so the frontier grew a second copy of
  every recurring idea — each with its own review history, and none of them
  saying it was the second. Retrieval stays full-text only: §5.4 defers
  semantic retrieval, and nothing here has an embedding, a similarity, or a
  notion of a closest record.
  - **The retrieval index gained a frontier surface.** Head revisions of every
    hypothesis, observation and finding chain, plus the operator's recorded
    review answers — §4.7 dispositions and the refinements they authorized —
    are indexed beside the corpus, in the same rebuildable cache file and
    through the same FTS5 match grammar, because a second copy of the grammar
    that turns untrusted text into an FTS5 expression is a defect this
    repository has already paid for. Reconciling is incremental by content: a
    record whose derived text is unchanged is skipped without touching the
    inverted index, a superseded wording is removed, and a candidate whose
    status moved is rewritten. `babel prepare` and every run reconcile it, so
    dedup and self-retrieval always answer about the same frontier. Index
    schema version 2; a version 1 cache is discarded and rebuilt, which costs
    one re-index of material derived from the corpus.
  - **`babel prepare` records the prior outputs related to a scope.** The
    prepared sessions' salient terms are computed mechanically during the pass
    that already digests them — occurrences weighed against how many records
    hold the term, so the boilerplate every transcript shares scores zero
    without a stopword list to maintain — and the top page of a frontier search
    with those terms is recorded in the preparation, bounded at twelve. The
    record holds kinds and ids only, in canonical order: the summary a run
    reads is derived from the record when the job is built, and sorting rather
    than ranking keeps §5.4's rule that retrieval rank is not evidence
    strength. Preparations carry a `serendipitous` marker for the conductor to
    set; `--serendipitous` sets it by hand. Preparations recorded before this
    release keep deriving their own ids, so a scope an operator already fixed
    stays explorable.
  - **Runs are told to refine rather than duplicate.** Every stage's job
    document carries the related records with one-line summaries and a framing
    that says exactly what they are: prior candidate ideas, untrusted, to be
    refined, revived, or amended by naming their record id instead of restated.
    A serendipity draw gets a different framing — inspiration, not constraint,
    and following the corpus somewhere none of them mention is the correct
    outcome. The five default-enabled lenses carry the same duty in their own
    wording, at version 2.
  - **A worker can search the frontier on demand.** `corpus-search` answers a
    `"scope": "frontier"` argument out of the same index, bounded by the same
    page limit and the same retrieval budget, redacted under the same
    disclosure class, and receipted: the trace step records the scope and the
    record identifiers it disclosed, because a frontier record is addressed by
    id and has no locator to cite. An unserved scope is denied rather than
    quietly answered with corpus hits. Every served page repeats the
    refine-first framing from the constant the job document uses.
  - **A near-duplicate candidate is recorded with a warning, never dropped.**
    Before writing a candidate, Babel searches the frontier for it and measures
    how much of the shorter statement's vocabulary the two share; past six
    tenths, an immutable warning naming the record it resembles is written in
    the same transaction as the candidate. The candidate keeps its wording, its
    status history and its place on the frontier — honesty over tidiness, since
    a duplicate recorded can be merged by a later revision and a duplicate
    silently discarded cannot be recovered. `babel explore` reports the
    warnings; the operator answers them.
- **Record actions in the browser: revision history, dispositions,
  process-further, and revive (issue #87).** #98 gave Babel actionable outputs
  and reached them only from a terminal; the web app — the primary surface —
  could read records and had no way to act on one. It now has the same four
  verbs, and they are the first writes the browser performs against the
  frontier's own state.
  - **A record page renders its chain.** `GET /api/record/revisions` reads a
    record's whole revision chain from any member of it, and the
    hypothesis and finding pages render it as a timeline: every wording, its
    author — a run or an operator, distinguished, because #87 makes the
    difference auditable — when, and why it superseded the one before it. A
    chain's first entry states that it supersedes nothing rather than showing
    an empty reason. The candidate's status history gains the same treatment:
    an operator's revive belongs to no run, and the timeline now names the
    person instead of rendering an authorless transition.
  - **Dispositions are answered where the record is read.**
    `GET /api/record/dispositions` lists the next actions proposed against a
    record with each one's ledger, and `POST /api/record/disposition/decide`
    appends an attributed authorization or decline. The response says what
    happened outside Babel — nothing — in a field rather than a comment, and
    the page repeats it: authorizing records that a person authorized an
    action and performs none of it. A draft-issue's rendered draft travels with
    the action and stays a closed disclosure until a reader opens it; the
    repository it binds to is shown beside it. Nothing here opens a link, and
    Babel still files nothing.
  - **"Process further" is one button and no text field.**
    `POST /api/record/invite` records #87's instruction-free nudge; the request
    body has no field an instruction could travel in, and the route refuses an
    unknown one rather than accepting it with the field dropped. The page shows
    the queued state and, once a run has taken an invitation, who took it.
  - **Resting statuses offer a revive, with a required argument.**
    `POST /api/record/revive` is offered on deferred, rejected and promoted
    candidates only, and the reason is required by both the page and the store:
    a candidate that can always come back is only safe if coming back leaves an
    argument behind.
  - **Every mutation confirms the wording the operator read.** Each of the
    three writes carries the chain head the page was rendered against, and a
    head that moved since is a 409 that names the current wording and says to
    reload — never a write. It is the one rule the web layer adds that the
    services do not have, because it is a fact about the page rather than about
    the store: internal/frontier cannot tell that the text under a button
    changed after the button was drawn, and an authorization attributed to
    someone who was shown different words is exactly the dishonest record #87's
    chain exists to prevent.
  - **The dashboard counts what is waiting and says why runs happened.** The
    review inbox gains a pending-proposed-actions count, kept separate from
    "awaiting a decision" because a verdict on a record and an authorization of
    an action about it are different questions. The receipt strip and the
    Explore run table render each receipt's authority; a receipt recorded before
    receipts carried one reads as "operator (recorded before authority)" rather
    than being filled in.
  - The web surface reaches all of this through two new narrow interfaces —
    `DispositionService` and a one-method `FrontierReviver` — so a browser
    request cannot propose an action, consume an invitation, or set a status,
    and the §14 structural test enumerates both.
- **Actionable outputs: dispositions, revision chains, invitations, and
  revive (operator direction 2026-08-31, issue #87).** Babel's records could
  be read and decided on; they could not be acted on, argued with, or nudged.
  A candidate's history was a chain of ancestor pointers with nobody's name on
  it, `rejected` and `promoted` read as endings, and the only thing an
  operator could tell Babel about a record was accept, reject, defer, or
  duplicate. Four durable additions, all inside "suggestions, never side
  effects": every one of them records a proposal or a decision, and none of
  them publishes, applies, or writes anything outside the durable file.
  - **Revision chains carry an author and a reason.** Every hypothesis,
    observation, finding, and proposal now has exactly one row in
    `frontier_revision` naming its place in a chain, who wrote it — a run
    receipt or an operator — and, for anything that supersedes an existing
    record, why. `babel revisions ID` reads the whole chain from any member of
    it, so an operator pastes whichever identifier a listing printed. The
    records themselves are unchanged: they were already immutable revisions
    linked by ancestor, so this is the editorial record that link could never
    carry, not a second copy of anything. The chain is single-successor —
    revising a record something already supersedes is refused, naming the head
    — because "current state = head" is only true when there is one head.
    Existing durable files are backfilled: a chain that predates this release
    reads as a chain, attributed to the runs that wrote it and carrying no
    invented reasons.
  - **`babel revise ID` is the operator's own edit.** A candidate's wording is
    the one payload a person can retype, so hand revision is confined to it;
    the structured payloads a run assembles are revised by runs. `--reason` is
    required, and the ancestor stays byte-identical and readable at its own
    identifier.
  - **Nothing closes: `babel revive ID`.** Deferred, rejected, and promoted
    are resting states with a transition out of each, taken by an operator or
    proposed by a run, always attributed and always argued. It is refused for
    a candidate that is untriaged, queued, or under investigation: the first
    two are already on the frontier and the third belongs to a running
    exploration. The resting status stays in the history — reviving argues
    with it rather than erasing it — and every status event now records its
    actor, because an operator's transition belongs to no run and `run_id`
    could not name one.
  - **Dispositions: typed next actions with an attributable ledger.** A new
    `internal/disposition` component holds proposed next actions against a
    record revision, in a closed vocabulary of five — `draft-issue`,
    `propose-reality-fact`, `store-memory`, `ask-question`,
    `develop-further` — each naming an existing Babel surface a click feeds,
    so a model cannot invent a sixth wired to nothing. A run proposes them
    through a new optional `dispositions` field on its result's candidates and
    consolidations, idempotent under the run's own emitted ref so a resumed
    run does not double every button; an operator synthesizes one with `babel
    disposition propose`. `babel disposition accept|decline` appends to an
    append-only ledger — attributed, timestamped, reconsiderable — which is
    the provenance later self-evaluation reads (#88, #94) rather than a new
    telemetry layer. Accepting authorizes and does not act: no issue is
    opened, no fact is written, and every result document says so.
  - **Draft issues bind only to a verified checkout (issue #88).** A
    `draft-issue` disposition requires `--repo DIR`, and the repository comes
    out of that checkout's own git configuration — origin URL, branch,
    existence — read from the files git writes, following a linked worktree's
    `gitdir` and `commondir`. git is never executed and GitHub is never asked:
    existence is proven by the checkout being on this machine, which also
    proves the operator works on it, and a repository nobody can point at is
    structurally impossible to name. The rendered draft is markdown on stdout
    and nowhere else.
  - **Invitations are instruction-free by construction.** `babel invite ID`
    records that a record deserves another look and offers no way to say what
    to do with it — refine, question, amend, or abandon stays the model's
    judgement (#87), and the table has no payload column an instruction could
    later appear in. `babel invitations` is the queue, oldest first and
    deliberately never re-sorted by a model-produced score. A run consumes each
    invitation exactly once, enforced by a consumption table's primary key
    rather than by a check, so two conductor cycles cannot spend the operator's
    budget twice on one nudge (#96, rung one).
  - Every new table carries UPDATE and DELETE triggers, matching
    `internal/frontier`: an append-only ledger whose append-only-ness depends
    on nobody writing the wrong statement is not append-only.
- **`docs/runbook.md`: the recovery, custody, and rollback procedures, run
  rather than written (issue #21, §14 gate).** The archive is worth exactly
  what its recovery is worth, and every procedure in it had until now only
  been reasoned about. Each of the gate's seven scenarios is now one section
  with preconditions, steps, verification, and the real output it produced on
  2026-08-31 against the production Cellar repository and managed catalog —
  exit codes, counts, digests, and timings, read-only throughout, with the
  live infrastructure identifiers replaced by placeholders because this
  repository is public.
  - **Repository recovery is proven twice, byte-identically.** `babel sessions
    fetch` restored a session belonging to a *different* host that this
    machine never held, and `restic restore` pulled the same subtree with no
    catalog consulted and no Babel code involved; `diff -r` between the two
    trees exits 0. That is the only form in which "archive recovery does not
    depend on PostgreSQL — or on Babel" is worth asserting.
  - **Two drills stay operator-gated, and say so with their commands.** The
    Bitwarden unlock/retrieve/relock ceremony needs the master password
    interactively; full restore-to-service needs a clean spare machine. Both
    are written out with the exact invocation and what success looks like,
    so neither is a procedure whose first real run happens during an incident.
  - **The drill found a live defect and declined to fix it.** Two stale shared
    locks from dead processes make `babel archive verify` exit 1 while leaving
    restores and integrity untouched — `restic check --no-lock` confirms 44
    snapshots, no errors. The remedy is `restic unlock`, a write verb, and a
    read-only drill that mutates the repository to make its own check pass is
    not a read-only drill. `archive verify`'s help now names that case and
    points at the runbook, because the CLI printing restic's raw lock error
    with no hint of where the remedy is documented is what made it worth
    finding twice.

### Changed

- **The web session is now a cookie the page cannot read (issue #72, SPEC.md
  §2.7, decision 34).** The launch credential was one 256-bit token generated
  at server start, delivered in the URL fragment, copied into `sessionStorage`,
  and presented as `Authorization: Bearer` on every request for the process's
  whole lifetime. Nothing about it was single-use and nothing rotated, and
  because it lived where script could read it, any future cross-site-scripting
  hole or compromised frontend dependency could have read it out of the page
  and used it elsewhere. §2.7 has required the exchange that closes that
  channel since the specification was written; it is built now, and the bearer
  path is gone rather than kept beside it.
  - **One nonce, one session, one use.** Each launch mints a 256-bit bootstrap
    nonce and prints it in the launch URL's fragment, which browsers never
    transmit. The page reads it once, erases the fragment, and posts it to
    `POST /api/bootstrap`; the server answers with a rotated host-only
    `HttpOnly; SameSite=Strict` session cookie and drops the nonce. Consuming
    the nonce and issuing the session happen in one step under one mutex, so
    two requests replaying the same launch URL cannot both be served — the
    property "single-use" would otherwise be a comment rather than a
    guarantee. A wrong nonce consumes nothing: a local process able to kill a
    live launch by posting rubbish at it would be a denial of service the
    256-bit nonce does not otherwise permit.
  - **The nonce expires two minutes after the launch.** §2.7 says quickly, and
    quickly is measured against what it replaces: a credential that stayed live
    in a terminal's scrollback for as long as the process ran. Two minutes
    rather than seconds because the link has to survive a cold browser start
    and an operator who pastes it by hand; the printed line states the lifetime
    from the enforced constant rather than repeating it, and every refusal names
    the one command that fixes it. A stale link is not a broken launch.
  - **The cookie is the only credential the server accepts.** No bearer header,
    no query parameter, no differently-named cookie, and not the spent nonce.
    That is asserted per channel rather than implied, because the value of an
    unreadable credential is exactly the absence of a second channel a later
    change could widen. Lock and stop revokes the session and the nonce
    together, so a launch link the operator never opened is worthless after a
    stop.
  - **`Secure` is deliberately absent, and the cost is recorded.** The origin is
    `http://` on 127.0.0.1: there is no network path to downgrade, and engines
    disagree about whether a loopback origin may set the attribute, so setting
    it would make the session work in some browsers and silently fail in others
    while defending against nothing. The same reasoning rules out the `__Host-`
    prefix, which requires it. What that leaves is a nuisance rather than a
    compromise: cookies are not isolated by port, so a page on another loopback
    port can overwrite this cookie in the operator's browser — it cannot forge a
    valid value, so the operator gets 401s and relaunches, and no attribute
    available to an `http://` origin prevents it.
  - **Nothing authentication-related is stored in the page any more.** The
    `sessionStorage` copy is gone, and a reload re-authenticates with a
    credential that was never reachable from script. The frontend's import-order
    coupling remains and is documented where it lives: the fragment is still
    route state, so the bootstrap still has to read it before the router mounts.
    An earlier record predicted this exchange would remove that coupling; it
    does not, because the only way to read the fragment earlier is an inline
    script in the served shell, which `default-src 'self'` does not admit.
  - **Proven at three levels.** internal/web drives the exchange, the replay,
    the expiry — including a clock that moves backwards — revocation, every
    cookie flag, the refused origins, and each channel the session must not be
    accepted from. A real browser bootstraps one launch and is refused the
    second use of the same link, and reads the established cookie out of
    Chromium's own store to prove `document.cookie` cannot see it. And the
    built binary was launched, exchanged, replayed and locked by hand.

- **`babel analysis profile configure` hands the operator the terminal instead
  of negotiating a profile over a pipe.** Every model invocation Babel makes
  has to trace back to an operator who intentionally set it up (operator
  decision 2026-08-31, issue #86), and the old ceremony could not carry that
  claim. It ran Code in the protocol's configuration mode over stdio, so
  whatever Code resolved a profile from — a dial passed straight through
  `--worker-arg`, an environment variable, a compiled default — became Babel's
  stored configuration with nobody present to choose it. It is a ceremony now.
  The worker is launched as `WORKER [ARG]... --configure --result-file PATH`
  with the operator's terminal on its stdin, stdout, and stderr; Code draws its
  own configuration interface there; the operator picks and confirms; Code
  writes the reference it saved to `PATH`; Babel stores that reference and
  prints the same summary as before. Nothing else is exchanged, and Babel still
  never sees the provider configuration behind the reference (SPEC.md §2.6,
  decision 18).
  - **No terminal, no configuration.** stdin and stdout must both be a
    terminal or the command refuses in one line, having launched nothing. The
    question is asked with an ioctl only a terminal answers rather than from
    the file's mode, because `/dev/null` is a character device too and a
    mode-based test would hand an interactive configuration to nothing. No flag
    substitutes for a terminal: automation binds a run to a profile that
    already exists with `babel explore --profile ID@REVISION`, which mints
    nothing, and there is no path left that mints one without an operator.
  - **A dial is refused rather than forwarded.** `--worker-arg` exists because
    Code speaks the worker protocol under a subcommand, so the executable has
    to be put into a mode; it was also a channel for handing Code a `--set`.
    Such an argument is now rejected — whether it was typed or stored by an
    earlier configuration, because a machine configured that way otherwise
    keeps reproducing it — and `$CODE_SELECTION_STATE` is removed from the
    worker's environment with a line saying so. The rest of the environment is
    inherited whole, unlike a supervised run's strict allowlist: this child is
    drawing an interface and needs `$TERM`, the locale, and wherever its own
    configuration lives.
  - **An abandoned ceremony changes nothing, and says which happened.** A
    worker that exits nonzero, writes no result file, or writes a file Babel
    cannot read leaves the settings document byte-for-byte as it was, reports
    `configuration unchanged` with the reason, names `babel analysis profile
    show` as the way to see what survived, and exits nonzero. A worker that
    writes a reference and *then* fails is the same case: the exit status
    decides, not the file. The result file is created empty and `0600` in a
    private directory Babel removes afterwards, so the operator's choice
    travels through a file Babel owns rather than one anybody could plant.
  - **A stored reference no longer carries metadata nobody can refresh.** The
    worker build, disclosure class, cost estimate, and capability list used to
    arrive on the same stdio channel the operator's terminal now occupies, so a
    record the ceremony mints has none of them, and the previous record's
    metadata is replaced rather than carried onto the profile that superseded
    it. `redaction_required` became nullable for the same reason: "Code never
    told Babel" and "Code said no redaction is required" are different
    documents, the second is a claim about what may leave this machine raw, and
    `analysis profile show` now prints the unknown one as absent instead of as
    a verdict. A document an earlier build wrote keeps displaying what it
    holds.
  - **`--timeout` is gone.** It bounded a protocol exchange. An operator
    reading Code's dials is not a hung process, and the ceremony ends when the
    operator or Code ends it.

  Verified against a real pty: the refusal on captured buffers, on a pipe, and
  on `/dev/null`; the full handover, with a stub worker recording that all
  three of its streams were terminals, that its argv carried Babel's two flags
  after its own, and that `$CODE_SELECTION_STATE` had been removed; every
  abandoned-ceremony path leaving the stored document unchanged; and the
  dial refusals launching nothing. The Code half of decision 1 is
  `atyrode/code`'s: configuration mode there stops resolving dials from
  `CODE_SELECTION_STATE`, `--set`, and compiled defaults. Babel's own
  conformance suite still grades the protocol's configuration mode, so a worker
  that has never been configured by an operator now fails that obligation —
  which is the honest reading of it.

- **Title inference goes through the same ceremony, and refuses until it has
  (issue #86, decision 2).** `babel sessions title infer` took the titler as a
  flag, and the reasoning written beside it was that a stored titler is one
  cron entry away from being automatic, so the spend should be chosen at every
  invocation. That answered a different question than the one the operator's
  principle asks. Typing a command on a command line is not a person deciding
  which model reads his sessions — an agent types flags, and nothing in the
  record afterwards can tell the two apart. What an agent cannot do is sit at a
  terminal. So the model that writes titles is now chosen exactly the way the
  analysis profile is, once, in Code's own interface.
  - **`babel titles configure` is that ceremony, reusing the launch #91
    introduced.** `WORKER [ARG]... --configure --result-file PATH` with the
    operator's terminal on all three streams, the same terminal requirement and
    one-line refusal, the same dial refusals, the same
    `$CODE_SELECTION_STATE` removal, and the same rule that an abandoned
    ceremony leaves the document byte-for-byte as it was. It stores the
    reference in the settings document's own `titles` block, beside the
    analysis profile and never inside it: two intentional setups, so
    configuring one may not decide the other, and each keeps the executable
    whose interface its operator actually confirmed — a reference means nothing
    without it, since two Code builds can both hold a profile named `p-3`.
    `babel titles show` prints the reference, when it was configured, and the
    launch it implies.
  - **Unconfigured, `--confirm` refuses before reading a single session.** The
    refusal names `babel titles configure`, says what that command does, and
    states that nothing was sent. Scanning the corpus first would spend the
    operator's time to reach a conclusion the settings document already held.
  - **Configured, inference uses exactly the stored reference.** The launch is
    `WORKER [ARG]... --titles --profile ID@REVISION` — the stored executable,
    the stored arguments, the confirmed profile — and no flag on the invocation
    contributes to it. `--titler` and `--titler-arg` are gone with the
    substitution they allowed. The disclosure preview now names the profile the
    material would go to alongside the launch, because that is the fact that
    decides whether sending it is acceptable, and each stored title records the
    launch that produced it, executable and reference together, so attribution
    survives a later reconfiguration of a document that is meant to be
    reconfigurable.
  - **The gate is on new spend, not on what was already paid for.** The five
    titles the operator's machine already holds keep their value and their
    honest `inferred` provenance, `sessions title clear` still withdraws them,
    and the preview — which reads local files and sends nothing — still runs on
    an unconfigured machine, because the operator deciding whether to configure
    this at all is exactly the person who needs to see what it would send.
  - `$CODE_SELECTION_STATE` is dropped from the titler's environment too, with
    a line saying so: a dial that overrode the profile an operator confirmed
    would defeat the ceremony from the other end.

  Verified against a real pty and a stub worker answering both launches: the
  terminal refusal on captured buffers, a pipe, and `/dev/null`; the handover
  storing the reference while leaving the analysis block untouched, and the
  analysis ceremony leaving the titles block untouched; five abandoned-ceremony
  paths, each naming `babel titles show` rather than the other configuration;
  the unconfigured refusal recording nothing while a previously inferred title
  keeps displaying as `inferred`; and a configured end-to-end run whose
  recorded argv is the stored launch, whose response is stored with the
  launch's own attribution, and whose title reaches `sessions list`.

### Added

- **What every session cost, recomputed from the transcript Babel already
  holds.** OMP writes a usage block into every assistant turn it appends to a
  session log — input, output and cache tokens, the priced cost of each, the
  model and provider that served it — and Babel threw all of it away on every
  describe. It is now summed at describe time and carried through the local
  catalog into the shared one, so an operator can ask which sessions were
  expensive, which were long, and which fought their tools, over a corpus
  whose only other handle was an opaque digest. On the operator's own machine
  that is 159 of 165 OMP sessions, $20,116.91 and 17.97 billion tokens across
  86,790 assistant turns and 5,071 tool errors, at zero inference cost: the
  numbers are arithmetic over bytes the archive already stores, so no model is
  invoked, nothing leaves the machine, and two runs over the same log produce
  the same document.
  - **Recompute, do not depend.** OMP keeps the same numbers in a per-turn
    ledger at `~/.omp/stats.db`, keyed by session file and entry id — exactly
    the path Babel ingested and the turn ids inside it, so a free and exact
    join was available. It was declined, because that ledger is garbage
    collected with OMP's local session retention. Measured on the operator's
    tree while building this: for one 90 MB session the transcript records
    6,595 priced turns and $1,178.30, and the ledger holds 5,957 turns and
    $996.44 — 638 turns and $181.86 it has already forgotten — while the
    corpus's single largest session has no ledger rows at all. The transcript
    is the durable source, and the ledger remains available as a cross-check.
  - **An absent measure says so.** A session whose log predates per-turn usage
    produces no aggregate and a completeness reason naming why, never an
    aggregate of zeros: a zero cost is a session that ran for free, and the
    six unmeasured sessions in the operator's tree are not free. The same rule
    runs the whole way down — nullable columns in both catalogs, `null` in
    `--json`, `-` in the table — and the shared catalog's new columns
    (`migrations/0006`: `cost_usd`, `total_tokens`, `turns`, `tool_errors`)
    refuse a negative value outright, because a negative measure is a broken
    writer rather than an unmeasured session. Codex and Claude Code sessions
    are untouched and publish NULL.
  - **The aggregate admits its own holes.** It reports how many assistant
    turns the log holds, how many carried usage, how many were priced, and how
    many records the scan could not read at all, so a partial sum is visible
    as one instead of under-reporting spend silently. A garbage line no longer
    truncates the scan, and a log read while OMP was appending to it degrades
    by one record rather than by everything after it.
  - **A narrow widening of the plaintext boundary, classed as what it is.**
    The four published columns are scalar measures of consumption and cannot
    be inverted into a word of what a session said, so §9's exclusions survive
    unchanged. What is new in kind is the money, and `internal/sharedcatalog`
    gives spend its own allowlist class rather than folding it into "size or
    count", so the contract's own listing shows a new disclosure as new.
  - **`babel sessions list` grows a COST column only when something recorded
    one.** A column of nothing but `-` costs every other column its width on a
    corpus whose harnesses record no usage, and a listing that hid a cost the
    adapter did read would send the operator to inspect sessions one at a time
    to find the expensive ones.

- **A dashboard at `#/`, and a Help page that says what Babel is.** The web
  interface opened on the session listing, which is the right page for the
  question "what did this machine record" and the wrong one for "what is Babel
  holding right now" — an operator had to visit six pages to learn whether the
  archive was current, whether the catalog had described anything, and whether
  anything was waiting on a human. The dashboard answers all six at a glance
  and then gets out of the way: every panel states its totals, shows the few
  most recent rows, and links to the page that owns them. It is a landing page
  rather than a seventh source of truth, so its numbers are the owning pages'
  own numbers, read through one new authorized endpoint, `GET /api/overview`.
  - **One request, six independently degrading sections.** A dashboard that
    refused the whole document because one store would not open would take
    away the five panels that had answers, which is the opposite of what a
    landing page is for. Each section carries its own availability and the
    server's own note — "no repository is configured", "the hypothesis
    frontier is not available in this session" — so a partial deployment reads
    as an explanation rather than as a failure. The review tile carries two
    sections, because the review log and the Reality ledger are different
    stores: a machine can hold one and not the other, and the question inbox
    stays on screen when the review log is gone.
  - **Nothing here starts work.** No route added by this change invokes a
    model, begins an exploration, or writes a record; the endpoint reads
    durable state the services already held. The one number the receipts could
    not supply — which recipe a run applied, which §9's plaintext allowlist
    keeps in the sealed body — is read from the frontier instead, because an
    observation records the recipe that produced it and the cookbook is
    public. Candidate counts per run come from the frontier for the same
    reason: a receipt records what a run did, and the frontier records what
    survived it.
  - **The framing survives the summary.** A panel showing candidates shows
    them in the model's own wording, untruncated, with the fallibility note
    beside them; the status distribution lists all six exploration statuses
    including the empty ones, so a frontier with nothing rejected still shows
    that rejection is a state records keep rather than a state they leave. The
    question inbox says its ranking is ordering estimates only, never
    evidence. Title coverage is split by provenance — recorded by the harness,
    derived by Babel, inferred by a model — because merging them would report
    a model's guess as the corpus's own name for a session, and an
    uncatalogued or catalog-pending count that was never read reports
    "unknown" rather than zero.
  - **A Help page at `#/help`, reachable from a persistent `?` in the header.**
    What Babel is and what it is not, the archive → catalog → prepare →
    explore → hypotheses → review lifecycle, the vocabulary a reader meets in
    the interface (session, revision, preparation, recipe, receipt, the two
    status vocabularies, provenance), a command-to-page map, and a quickstart.
    It reads no API at all, so it renders on a machine where nothing else
    does — which is exactly when an operator needs it. The dashboard points at
    it once, on a first visit, with a dismissible one-line banner and a
    `localStorage` flag; there is no tour and nothing to click through.

- **`babel archive fleet`, which answers "did all my machines back up" in one
  command.** `archive status` already reported each host's newest snapshot
  time, and that turned out to be the raw material rather than the answer: a
  host six days stale rendered identically to one that published minutes ago,
  apart from a timestamp the operator had to compare by eye. Worse, a machine
  that had stopped publishing entirely was invisible, because the listing is
  derived from what is in the repository and you cannot miss what was never
  there. The new command states the verdict — `current`, `LATE`, `MISSING` or
  `unknown` — with a one-line summary above the table, so the question is
  answered by glancing rather than by arithmetic.
  - **"Late" is derived, never assumed.** Babel does not own the archive timer
    and its configuration does not record that timer's schedule, so no
    threshold here pretends to know one. Each host is judged against the median
    gap between its own most recent snapshots — the median rather than the mean
    precisely because a mean is dragged upward by the outages the command
    exists to notice, so a host down for a week would derive a cadence that
    excuses being down for a week. A host with too little history of its own
    borrows the fleet's median, and the `EXPECTED EVERY` column always names
    which of the three sources was used, so a number Babel inferred can never
    be mistaken for one it was told.
  - **Three missed publications, not one.** The Phase A timer is
    `Persistent=true` precisely so a machine that was asleep or offline catches
    up on the next run, so one missed run is the mechanism working. Two
    consecutive misses is a pattern rather than an event, and that is the
    smallest distinction between "this machine is broken" and "this machine had
    a bad hour". A cadence observed faster than hourly is treated as hourly:
    SPEC.md §12 fixes hourly as the Phase A schedule, so a faster rate is
    bootstrap pushes rather than a schedule, and without that floor a machine
    the operator pushed twenty times by hand would have read as late an hour
    later — a false alarm produced entirely by Babel's own inference.
  - **A host with no derivable cadence gets no verdict**, reported as `unknown`
    with its age still shown. Calling it `current` would be a guess wearing the
    word that means "your backups are fine", which is the one failure that
    would make the command worse than the timestamps it replaces.
  - **No stored roster, deliberately.** A machine that has never published is
    invisible to both authorities by construction — the catalog learns of a
    host by way of its first publication, so `hosts` rows only ever appear
    alongside a snapshot restic already committed, and a table derived from what
    was archived can never name a machine that archived nothing. Absence
    therefore has to come from outside the archive, and it comes from the
    operator at the moment he asks: `--expect ws-linux,wsl-nixos` reports an
    absent machine as `MISSING`. A roster Babel persisted would be state that
    goes stale silently and then answers confidently, and a fleet list that has
    quietly stopped matching the fleet is worse than no list, because it turns
    "I do not know" into a wrong answer. An expectation passed in the invocation
    cannot rot between invocations. It only ever adds machines, never hides one:
    a host publishing normally is reported whether or not it was named.
  - **It exits 0 whatever it finds, including a late or missing host.** "Late"
    is a judgement derived from a cadence Babel inferred rather than a fault it
    observed, and an exit code is a contract scripts and timers come to depend
    on — which is the alerting system this deliberately is not. It answers when
    asked and does nothing between; `--json` carries a per-host `state` for
    anyone who wants to script it anyway.
  - Verified against the live shared archive read-only (25 snapshots, one host):
    `current` and `MISSING` were both observed there — the operator's second
    machine has genuinely published nothing, so its absence is a real finding
    rather than a fixture — while `LATE` and `unknown` were constructed, the
    latter also live against a throwaway local repository. Each distinction the
    tests assert was checked by removing it from the source and confirming the
    test fails.
- **Session and host metadata in the shared catalog, so browsing the fleet
  returns something a person can read.** The operator hit the gap directly: his
  WSL machine's `babel web` showed 5 sessions while the archive holds 838 from
  `workstation-linux`, and even once cross-host browsing landed those 838 rows
  had no title and no workspace, because the catalog never stored them — they
  existed only in each machine's local SQLite description cache. Migration
  `0004_fleet_metadata` adds `title`, `workspace` and `continuation_grade` to
  `babel.sessions`, and `display_name`, `os`, `arch` and `identity_updated_at`
  to `babel.hosts`, and both push paths now populate them.
  - **This widens the plaintext boundary, by explicit operator decision
    (2026-08-30).** He was shown the tradeoff — transcripts stay encrypted in
    restic while the catalog is plaintext in a managed provider's PostgreSQL,
    over a TLS connection that is encrypted but not authenticated — and asked
    specifically about titles versus workspaces, chose both: "the richer, the
    better." The permission is that column set and does not generalize.
    Transcript bodies, plaintext full-text indexes, deterministic ciphertext,
    session selectors and adapter source ids, and a machine's system hostname
    all remain outside the allowlist, and the drift test that used to prove
    `sessions.workspace` was rejected now proves `sessions.primary_path` and
    `hosts.hostname` still are — a widening by column rather than by class.
  - **Decision 8 was a recorded drift, and is now implemented rather than
    deleted.** SPEC.md said "host display names are catalog rows where the
    newest value wins" while no such column existed; the only occurrence of
    `display_name` in the repository was a test asserting it would be refused.
    Newest-wins is an in-place update and no history is retained, and the spec
    text was corrected to say so: an audit trail of former names is a different
    feature, and a rebuildable catalog could not honestly keep one, since a
    rebuild reconstructs rows from the repository and would erase the trail it
    claimed to hold. `hosts.created_at` already is the first-seen time —
    server-assigned, never rewritten, preserved across a rebuild — so no
    `first_seen_at` column was added to disagree with it.
  - **NULL means unknown, never false and never empty.**
    `continuation_grade` is a nullable boolean because only the publishing host
    can resolve it from its own files: `NOT NULL` would force absence to render
    as "this session cannot be continued", a verdict nobody reached, and a
    cross-host reader meets that absence routinely.
  - **The 838 existing rows are backfilled by a push, never by a rebuild.**
    `storage rebuild` deletes a host's session rows and cannot reconstruct them
    from a snapshot listing, so it is the wrong tool and its help now says so;
    it does preserve host identity, because it has no way to know another
    machine's facts. The operator's own action is to update his Nix profile so
    the hourly timer runs a binary carrying `0004`.
  - **`SchemaVersion` stays 1, deliberately.** Every added column is nullable
    with no default and no constraint, so a writer that predates the migration
    keeps publishing; raising the version would instead order every un-updated
    instance to stop. That was demonstrated rather than asserted: the
    operator's timer binary predates even `0003`, and its scheduled unattended
    run published 838 sessions successfully against the migrated schema and left
    the metadata a newer push had written untouched.
  - **Fixed alongside it:** `storage verify` probed `public.deployments` for the
    recorded schema version, and every catalog object lives in `babel`, so the
    probe always failed and a registered deployment always reported "not
    recorded yet". It now reads 1 against the live add-on.
- **A real execution sandbox for the analysis worker, which is what stood
  between Phase A working and Phase B being able to run at all.** Babel
  refuses any worker that cannot declare filesystem isolation, egress denial,
  resource ceilings and disposability; Code declared backend `process` with all
  four false, honestly, so every exploration was correctly refused. The
  operator settled three choices and both halves were built against them.
  - **The Linux backend is `bwrap --unshare-all --die-with-parent` inside a
    `systemd-run --user --scope`** — two mechanisms because neither covers both
    halves, since bubblewrap has no resource accounting and a transient scope
    has no isolation. Every declared property is read back off a live probe
    before it is declared: a scope whose cgroup lacks the requested ceilings
    degrades the declaration rather than inflating it, and a run whose ceilings
    vanish after a declaration claiming them is torn down, because the claim has
    already gone out on the wire.
  - **The sandbox has no network at all.** Egress is host-side unix sockets
    bind-mounted in: a `CONNECT` proxy Code owns, allowlisting exactly the
    resolved provider endpoint, with an in-sandbox forwarder so OMP's
    documented process-wide `PI_PROXY` hook carries it unmodified. A second
    relay was discovered to be necessary while implementing rather than while
    designing: the auth broker is a loopback service, OMP never proxies a
    loopback target, and the sandbox's loopback is its own — so a networkless
    run would have authenticated against nothing.
  - **Darwin gets no qualified backend, so exploration is refused there** while
    archival, verification, fetch, catalog, web and review continue untouched.
    Its only unprivileged mechanism is a deprecated `sandbox-exec` with no
    cgroup or mount-namespace equivalent, so two of the four properties could
    not be declared without lying. Babel now refuses on an unqualified platform
    whatever the worker declares, because §10 declines to take that declaration
    on faith, and the refusal explains itself rather than reading as a fault.
  - **Six escape scenarios drive the real launch path and attempt the violation
    each property forbids** — a host write outside the grant, a non-allowlisted
    `CONNECT` and a direct connect with no route, a fork bomb, a memory hog,
    survival past teardown, an unreaped tree. Each is proven non-vacuous by
    relaxing the control it tests, and a prerequisite guard fails loudly rather
    than letting the scenarios skip into a green suite: dropping `net` from the
    unshare set makes it report the 11 routes that leaked and name every
    property that consequently went untested.
  - **The threat model is written down** in `docs/sandbox-threat-model.md`,
    with each property mapped to its mechanism, the prerequisites and their
    absence behaviour, and eight ranked residuals — headed by exfiltration to
    the one reachable endpoint, the provider credential living inside the
    sandbox because OMP authenticates, and the local broker being reachable for
    the life of the run. Code's `escape` string and that section are the same
    account at two levels of detail and may not contradict each other.
  - **Code now scores 14/14 against the conformance suite under the strict
    requirement**, with both non-worker controls at 0/14. `run/reports-resources`
    failed first and was right to: Code had started bounding memory, CPU, tasks
    and disk space while reporting nothing, and a bound is enforced by measuring
    what it bounds. Figures now come off the scope's `cgroup` (`cpu.stat`,
    `memory.peak`) with each dimension carrying the file or syscall it was read
    from, so a number cannot exist without naming its source; an unmeasurable
    dimension is omitted rather than zero-filled, because a zero reads as a
    measurement.

- **The web interface now says whose sessions it is showing, and can browse
  and fetch another host's.** An operator ran `babel web` on a second machine,
  saw 5 sessions with `/tmp` in every workspace cell, and concluded the list
  was scoped to the folder he had launched from — while the archive one click
  away held 838 sessions from another host and the page never connected the
  two. Both halves of that are the same defect: the surface stated no scope and
  offered no way out of it.
  - **The Sessions page states its scope in the heading and in a notice that
    reads differently for each of the four ways an archive can stand.** No
    repository configured says local is everything and explicitly claims
    nothing about an archive; configured but unreadable says the question is
    unanswered rather than the archive empty; sole publisher says there is
    genuinely nothing else and the operator is not missing anything; another
    host publishing names it and its snapshot count. The `Workspace` column —
    the thing that produced the wrong reading — is now `Recorded workspace`,
    because naming the host fixes "what is this list" and leaves "why does
    every row say /tmp" live.
  - **`GET /api/archive/sessions?host=ID[&snapshot=ID]` exposes the CLI's
    `sessions list --host`,** which reads a snapshot's file listing and
    downloads no transcript bytes. It is a separate route from `/api/sessions`
    because that one answers from an in-memory catalog, cannot fail for
    repository reasons, and is polled during a scan. Its rows are a separate
    type that cannot carry title, workspace, modified time, or continuation
    grade, so a client has no way to render an unobserved field as a blank
    cell; the interface prints "not in listing" with the reason.
  - **`POST /api/fetch` takes a host,** so a selector discovered in another
    machine's archive is actually recoverable — without it the selector
    resolves against local files that by definition do not hold it. Each
    archive row reports whether this machine already holds a materialization,
    and a fetch flips that from the server's own answer rather than the page's
    assumption.
  - **`GET /api/state` no longer blanks the host id when no repository is
    configured.** The repository is still withheld, because there is none, but
    whose machine this is has an answer either way and the page needs it.

### Fixed

- **Corpus search answered a worker's keyword query with nothing, because the
  terms were ANDed.** An analysis worker sends a bag of words, not an
  expression. `internal/index` translated one into an FTS5 conjunction, which
  asks for a single transcript record holding every word — a question a corpus
  of individual records essentially never answers yes to. The operator's first
  exploration retrieved four times against a healthy index of 26,948 events
  and was served 0, 0, 1 and 0 hits, while every individual word in those
  queries matched between 32 and 4,683 records. Terms are now optional and the
  result is a union.
  - **Relevance carries the weight the conjunction used to.** A union that
    served everything would have replaced one failure with another, so
    membership is broad and order is discriminating: FTS5's bm25 already
    scores a record by how many of the query's phrases it matched and weighs
    each by how rare it is, `Query.Limit` bounds the page at fifty by default,
    and a query with no bearing on the corpus still matches nothing at all.
    The whole query is also tried as one adjacent phrase alongside its words,
    so a record holding the caller's phrasing outranks one that merely holds
    the same vocabulary scattered.
  - **A query is bounded as well as sanitized.** Every optional term widens
    the candidate set where an intersection narrowed it, so at most 32 terms
    of an expression reach FTS5. A longer query is answered on its first 32
    rather than refused: a query never fails on its content.
  - **An unsearchable query is an answer, not a failure.** A query holding
    nothing a tokenizer could match, or one past the index's length bound, is
    now served as zero hits with a reason and recorded in the retrieval trace,
    so a worker can tell "the corpus does not hold this" from "Babel would not
    look", and the receipt still shows the retrieval it spent.

- **An alignment audit of the whole system against `SPEC.md`, and the defects
  it found.** Seventeen read-only audits covered every package, both
  counterpart repositories, and the deployment, then drove the shipped binary
  through the full Phase A lifecycle against the real 838-session corpus. The
  lifecycle held end to end. What it found was concentrated in the places
  nobody had exercised, and two findings were the same shape: a proof that
  could not fail.
  - **The conformance suite graded a stub whose behaviour had diverged from
    the code path it stood in for.** Babel requires every analysis result to
    declare `babel.analysis-result/1` and fails closed otherwise; Code's real
    investigator declared `code.investigation.v1`, so every real analysis run
    would have been rejected after the work was done. Conformance never saw it
    because it grades a conformance stub, which emitted the right string, and
    no obligation read the result schema. The schema string now has exactly one
    definition on each side of the wire, `run/well-behaved` asserts it, and the
    divergent constant is deleted rather than corrected so it cannot drift
    again.
  - **`run/no-credential-leak` certified workers that never ran.** It searched
    a rendered receipt for a token that nothing had ever placed, so it could
    not fail: `babel conformance /bin/true` passed it while failing the other
    ten, and the suite's own all-fail test explicitly exempted it. Two further
    layers of the same defect surfaced on the way down — the receipt was
    rendered with `%+v`, which prints the nested result as a pointer address
    and never reached the payload, and the first redesign (grade the worker's
    raw bytes) would have relocated the vacuity rather than closing it, since a
    worker emitting nothing passes a test for an absence trivially. The
    obligation now drives the worker with a directive that asks for the
    credential back, and holds only on three conjoined facts: the run reached a
    terminal result, the token appears in no byte the worker wrote, and it
    appears nowhere in the receipt. Four negative controls pin it, and the
    all-fail test now exempts nothing.
  - **`babel explore --recipe` validated the operator's scoping and discarded
    it.** Both branches returned the unnarrowed set, so every run analysed all
    eight recipes and the receipt attested all eight — a receipt overstating
    what was analysed, which is the one thing this product exists not to do.
    `Set.Defaults()` had been written and never wired up; its only caller was
    its own test. Selection now narrows in one place and reaches both the
    worker's brief and the receipt, and a recipe named explicitly runs whether
    or not it is default-enabled.
  - **The vault session token travelled on argv.** `atyrode/dotfiles`'
    storage ceremony exported `BW_SESSION` and then passed `--session` on every
    `bw` call anyway, putting a token that grants full vault access into a
    world-readable process listing — contradicting the script's own header and
    `SPEC.md`'s rule that secrets never enter argv. Removed at all five call
    sites, with a check that fails if it returns.
  - **The archive timer armed before there was anything to archive.** Home
    Manager enabled `babel-archive.timer` at activation, inverting the §12
    rollout order in which storage is configured and verified first. The unit
    now carries a start condition on the storage document, and the ceremony
    arms it on success — because a start condition alone would leave a
    configured machine inert until its next login.
  - **`Config.Redacted()` omitted the object-store credentials** while
    documenting itself as safe to print. No shipped path called it, which is
    why nothing leaked; it was a trap set for the next caller. It now covers
    every secret-bearing field, and a reflective test fails when a new one is
    added without a decision.
  - **Four deployment-critical commands were missing from `babel --help`** —
    `archive init`, `storage migrate`, `storage verify`, `storage rebuild` —
    including the one named by the error an operator hits first. A test now
    derives the command list from the dispatch source and fails if any
    reachable command is undocumented.
  - **Trusted inventory import had no operator command.** `ImportFacts` was
    implemented and tested, and `babel reality import` now reaches it. It
    deliberately takes no `--operator`: the ledger attributes imported facts to
    the trusted source, and collecting an operator identity would imply they
    personally authorized what §4.8 attributes elsewhere.
  - **Smaller gaps closed:** `internal/digest` was the only package with no
    test at all; the frozen migration tests pinned names and order but not
    bodies, so editing an applied migration in place passed; `go test -race`
    was in no gate (it is now a parallel CI job, and the tree was already
    clean); the browser leak suite skipped silently without Chrome; `storage
    configure` documented no schema and accepted an insecure password file in
    silence; and `sessions fetch` failed without naming `--host`, the flag that
    recovers a session this machine no longer holds.
  - **An operator-requested `babel web` lock could exit non-zero having
    worked perfectly.** Found by the new race gate on its first CI run, which
    is the whole argument for the gate. `shutdown` closes the listener, then
    `http.Server.Shutdown` closes the listeners it still tracks — the same one
    — and reports closing an already-closed listener as its error, unless
    `Serve` returned first and untracked it. Which one wins is scheduling: on
    an idle machine `Serve` wins and the bug is invisible, and forty
    consecutive local runs passed. On a runner with four concurrent jobs
    `Shutdown` won. An already-closed listener is the state the lock asked
    for, so it is no longer read as a failure, and the regression test runs
    the lock cycle enough times for the loser to change.

### Changed

- **`SPEC.md` no longer claims more than the code does.** Four dated claims
  overstated what was built, and each is now corrected with the measured
  property and, where a gap remains, a recorded gate. Containment is refused at
  the worker's first event — before any analysis executes, but after the job
  document with its broker token has already reached the process, so the staged
  form that would close it is now a named gate. The restic port is eight verbs,
  not the six enumerated in three places; `cat config` is how a missing
  repository is told from an unopenable one, `ls` serves selective retrieval,
  and the property that matters is that no verb in the set can delete or
  rewrite repository data. §8's command table was undercounting the shipped
  surface. And decision 34's "one-time session bootstrap" describes a bearer
  token reused for the server's lifetime and readable from JavaScript, not the
  rotated `HttpOnly` cookie §2.7 requires; the exchange is recorded as a gate.

### Added

- **`babel conformance WORKER` sits any binary down in front of the
  analysis-worker contract suite.** The obligations Babel holds a worker to
  lived in `internal/`, which Go forbids another repository from importing, so
  the one program that most needed to take the exam could never reach it. They
  are now values rather than inline subtests, driven either by `go test` or by
  the command, which prints one line per obligation with its failures beneath,
  emits the same report under `--json`, and exits non-zero unless every
  obligation held. The suite also stopped assuming a worker speaks the protocol
  at `argv[0]` — `--worker-arg` puts an executable into worker mode, so an
  interactive program that answers under a subcommand is graded as itself
  rather than through a wrapper script. Babel's own tests run the identical
  list against the fake worker, so the exam and the implementation still cannot
  drift apart. `--unsandboxed` grades against the relaxed containment
  requirement, because a worker that has not built a sandbox yet fails every
  worker-mode obligation with the same containment error and the report cannot
  otherwise distinguish that from a worker that does not speak the protocol;
  the relaxation is always stated in the output, and
  `run/declares-containment` fails either way.
- **Phase B orchestration and durable surfaces.** `internal/explore` is the
  §6.5 run controller: preflight, then discovery, then development, then a
  logically separate challenger, then a synthesizer, with a resume ledger that
  binds each worker-emitted reference to the durable record it produced so a
  cancelled run resumes without duplicating. Every candidate is persisted
  before any sorting, because §5.2 requires a finite run to defer the remainder
  rather than erase it. `internal/reality` is the §4.8 ledger: entities whose
  merges are genuinely reversible, immutable fact revisions with
  predicate-separated lifecycle, ownership and analysis policy, scoped trusted
  sources, versioned focus rules that must be evaluated rather than inferred
  from lifecycle, the nine-state question machine, and plans whose fact
  mutations wait for one explicit operator acceptance. `internal/review` is the
  §4.7 service: append-only dispositions with attributed context that can never
  satisfy an evidence requirement, durable-learning assessments whose memory
  proposal is disposed independently of the revision it accompanies, lineage in
  both directions, and export whose Markdown is inert. `internal/sharedcatalog`
  gains the Phase B object-first commit protocol, sealing payloads before they
  reach PostgreSQL and leaving an interrupted sync visibly `pending-sync`.
- **The Phase B analysis core**, still on synthetic data only. `internal/index`
  is the provenance-preserving retrieval §5.4 requires and no more: SQLite FTS5
  over source records with structured, temporal, and repository-path filters,
  and deliberately no score, rank, or relevance field, because §5.4's rule is
  that retrieval order never becomes evidence strength. `internal/frontier` is
  the durable hypothesis frontier, where the invariants are structural rather
  than validated — a hypothesis has no status column, only append-only status
  events, so no code path can overwrite a lifecycle; there is no delete
  statement anywhere in the package; an observation row carries a
  `CHECK(evidence_count > 0)` so §4.3's evidence rule survives payload
  encryption, since a store that cannot read sealed evidence can still refuse a
  row claiming none; and a refinement request cannot exist without the
  rejection that authorized it. `internal/cookbook` loads versioned recipes
  through a hand-rolled strict front-matter grammar, ships the five
  default-enabled lenses and three drafts §5.5 names as real analytical
  guidance, and enforces §5.1's version rule with a drift check whose digest
  excludes the version field — otherwise an increment would move the digest and
  the check would have no signal. `internal/run` holds immutable preparation
  records with domain-separated derived IDs and the §7 run receipt, split into a
  plaintext-eligible header and a sensitive body so a deployment that seals
  bodies can still list, order, and chain receipts without a key.
  `internal/preflight` is the deterministic secret and health preflight, whose
  findings carry locators and placeholders but never a secret value.
- **Containment is declared rather than assumed.** A worker's resolved
  configuration must now name its sandbox backend, its filesystem, network,
  resource, and teardown properties, and its own escape assumption, and Babel
  refuses a declaration short of the run's requirement before any job material
  reaches the worker. The strict requirement is the default so an unset field
  fails closed, and even a relaxed run must still name a backend, because a
  receipt that names no boundary cannot tell a reviewer what the evidence was
  produced behind.
- **The four Phase B foundations**, each buildable and testable with no real
  data, no credentials, and no network. `internal/event` is the SPEC §4.1/§6.3
  analysis event model: a streaming per-harness classifier into user reports,
  agent claims, tool observations, repository changes, and verification
  evidence, where an unrecognized or damaged record degrades to an opaque
  partial event and is never dropped. Its 16 MiB record budget is a measured
  constant, not a guess: real harness logs carry single records into the tens
  of megabytes, where a `bufio.Scanner` default would degrade about one record
  in a hundred. `internal/synth` generates a deterministic synthetic corpus
  whose extremes exceed production — a primary log past 320 MiB and a record at
  the budget ceiling — because a fixture smaller than production is a fixture
  that lets production break the reader. `internal/envelope` is the client-side
  AES-256-GCM payload envelope whose additional data binds a ciphertext to its
  row, type, and field, so an envelope moved between rows fails to open, with a
  keyring that seals under one key and opens under every known one.
  `internal/worker` defines the Code analysis-worker protocol, implements
  Babel's whole side of it — version negotiation, per-request authorization
  against the run's grant, process-tree lifetime, output validation, receipts —
  and ships the `Conformance` suite that a real worker must pass, since Code
  does not implement the counterpart yet.
- **A `packages.default` flake output**, so a scheduler in another flake can run
  a pinned Babel from an absolute store path rather than whatever `PATH` holds.
  `version.go` accepts a link-time revision because a Nix build compiles from a
  source copy with no `.git`, where `-buildvcs` stamps nothing; with nothing
  injected the reported identity is byte-identical to before.
- **`archive push --json` reports `snapshot_id` and `incomplete`** as the fields a
  scheduler needs to tell three outcomes apart: a push that archived nothing
  because the host has no source roots, one that archived only part of a tree, and
  a complete snapshot. Exit status alone conflates the first with the third.

- **The two §14 pre-deployment schemas are frozen, and the freeze is
  enforced by tests rather than asserted in prose.** `storage.json` at
  `config_schema` 2 and the catalog at `SchemaVersion` 1 with migrations
  `0001_init` and `0002_unknown_counts` — both having run against real Cellar
  and the real managed PostgreSQL, which is the strongest evidence they were
  going to get before carrying data.

  `internal/config` pins the exact JSON names at every level, the schema
  number, and that a pre-freeze schema-1 document still loads. That last one
  matters because loading *deliberately* ignores unknown names so a newer
  writer's document stays readable — which is precisely why no other test could
  catch an accidental field. Adding a field now fails with both sets printed.

  `internal/sharedcatalog` pins the migration ledger's identity and order, the
  schema version, and the allowlist's table set with a non-vacuity check. The
  schema changes by adding a migration, never by editing one that has run: an
  applied migration is history, and rewriting it leaves every deployment that
  ran it holding a shape nothing describes.

  Frozen does not mean unchangeable. It means a change is a deliberate act
  with a compatibility story, and the way to have that conversation is for
  these tests to fail.

- A `flake.nix` dev shell pinning the full toolchain (Go, restic, Bun,
  PostgreSQL client, `CGO_ENABLED=0` matching the release builds), so
  `nix develop` replaces ad-hoc `PATH` exports to nix store paths ([#24]).
- The Phase A shared catalog schema and migrations: deployments, instances,
  hosts, snapshots, opaque session identity, server-time fenced host leases,
  and idempotency keys, applied transactionally from embedded SQL ([#25]).
- **`archive push` now publishes to the shared catalog.** Until now shared mode
  was configurable but inert: nothing registered a deployment, host, or
  instance, and no code path called publication or leases at all, so an
  operator's machines could never appear in the shared catalog. A push now
  registers its identity, takes a server-time fenced host lease, publishes the
  snapshot with its session identity rows, and releases the lease. The restic
  snapshot id keys the publication, so a retried push of the same snapshot is a
  no-op rather than a duplicate.

  Only opaque identity crosses the boundary: a session's uid is a digest over
  deployment, host, harness, and source id, and the source id - which embeds a
  workspace-derived project slug - never leaves the machine (SPEC.md §9).

  **An outage defers rather than fails.** The snapshot is already durable in the
  repository, so a push that cannot reach PostgreSQL reports `uncatalogued` and
  exits 0, and the next push adopts it from the repository's snapshot listing.
  A lease another instance already holds defers the same way, for the same
  reason. What does *not* defer is a refusal: a rejected credential, a missing
  privilege, a pending migration, or a schema this binary cannot write all fail
  loudly, because reconciliation would hit the same wall and reporting a state
  that appears to resolve itself would hide a misconfiguration. The rule is
  whether PostgreSQL answered.

  The word is `uncatalogued`, matching `archive status`, and deliberately not
  `catalog-pending`: that phrase names a different state in this system — a row
  that exists, carries restic's real counts, and lacks any record of which
  sessions the snapshot held. A push that used the narrower word for the wider
  state would send an operator hunting for session detail that was never
  written rather than for a row that was never created.

  **Reconciliation runs before publishing, not after**, which is load-bearing
  rather than stylistic. `Reconcile` assigns each adopted snapshot the next
  order above the current maximum, so adopting a stranded *older* snapshot after
  publishing a newer one would give the older one the *higher*
  `publication_order` - and that column exists so readers can select the newest
  snapshot without trusting clock skew. Two tests pin it, one asserting the
  invariant and one demonstrating the inversion the sequence avoids.
- Session closure counts (artifacts, blobs, unresolved blob references) are
  cached in the local session catalog, at schema 2. Publication needs them on
  every push, and re-describing an unchanged session to recover a number the
  describe already computed would make an hourly push scale with the whole
  corpus rather than with what changed. A cache at the old schema is discarded
  and rebuilt, which is safe: every row derives from live sources.
- **`archive status` reports how far the shared catalog is behind**, so
  `catalog-pending` is observable between pushes rather than only in the output
  of the push that deferred. It reports whether the catalog is reachable, how
  many snapshots are archived but uncatalogued, and how many are recorded
  without session rows.

  SPEC.md §9 promised an idempotent local `catalog-pending` journal; that is now
  scoped to Phase B's `pending-sync`, and Phase A derives the answer instead.
  The repository is authoritative for which snapshots exist and the catalog for
  which it recorded, so their difference *is* the state. A third local copy
  could be lost with the rebuildable local database, or go stale the moment
  another instance reconciles, and would then disagree with both.

  An unreachable catalog leaves the counts **absent rather than zero**:
  reporting 0 uncatalogued snapshots is a claim the command cannot make without
  reading the catalog, and the terminal output says `unknown`.

  The two counts are **not interchangeable**, and the difference decides what
  an operator should do. An uncatalogued snapshot has no catalog row, which is
  what an outage leaves behind, and the next push records it. A
  `catalog-pending` row already exists with real counts from restic but no
  record of which sessions the snapshot held — and no shipped command resolves
  that, because pushing again publishes the next snapshot rather than
  completing this one, so the count does not fall. The archive is unaffected:
  those snapshots stay durable and restorable, and only catalog detail about
  them is missing. Completing it needs a restore-and-rescan, now an explicit
  Phase C item rather than something SPEC.md implied a push would do.
- **Babel owns its own PostgreSQL schema** (`babel`), created by `storage
  migrate` and pinned as `search_path` on every connection (decision 47).
  Driving the real Clever Cloud add-on showed why: it pre-installs 40
  extensions, and PostGIS and `pg_stat_statements` put 7 relations in `public`.
  The allowlist gate — which makes the SPEC.md §9 plaintext boundary
  enforceable — saw those as unauthorized tables and failed the migration
  *after* it had applied. Sharing a schema leaves no way out: rejecting a
  provider's extensions is wrong, and ignoring unknown tables blinds the gate
  to a Babel migration adding an unlisted one. An owned schema keeps it exact.
  An unknown table is now also named once rather than once per column.
- **Shared mode in `storage.json`, at schema 2.** The document now carries
  `mode`, deployment/instance identity, and a `catalog` section: PostgreSQL
  endpoint, TLS mode with an optional root CA, one credential by default, and
  an optional separate migration credential where a provider can issue one. A
  schema-1 document loads as local mode, so existing configurations keep
  working; a newer schema is refused by name. Golden fixtures cover local,
  single-credential shared, and separate-migration-credential documents.
- **Credential privileges are detected rather than assumed** (SPEC.md §9,
  decision 46). `storage verify` reports what the credential is observed to
  hold — superuser, role-creating, DDL, or application — read from PostgreSQL's
  own catalogs with no destructive probe. Role attributes are not inherited
  through membership in PostgreSQL, so the check tests reachability by
  `SET ROLE` rather than inherited usage; the inherited-usage form silently
  reports a `NOINHERIT` member of a superuser role as an application
  credential, which was confirmed on a throwaway cluster before choosing.
- `storage migrate`, which applies pending migrations with the configured
  credential by default, or with an ephemeral document's migration credential
  that is used and never persisted. `storage verify` checks a configured
  catalog live: TLS as the server reports it, observed privileges, and schema
  compatibility.
- **The SPEC.md §9 plaintext allowlist is now enforced, not documented.**
  Every shared-catalog column is enumerated with its permitted data class, and
  `Verify` reflects the live schema and fails on any column or table outside
  it. `Migrate` runs it before reporting success, so a migration that would
  widen the plaintext boundary fails at apply time. Sessions are keyed by an
  opaque digest rather than their selector (which embeds a workspace-derived
  project slug), hosts carry no display name, and no session quality verdict
  is stored ([#25]).
- Migration and application role separation for the shared catalog, usable only
  where a provider permits creating database users: an application role
  receives DML on catalog tables, read-only access to the migration ledger, and
  no DDL, so a normal instance cannot change schema or claim a migration it did
  not apply, and credentials are per-instance and revocable without disturbing
  other instances. DDL identifiers and passwords are quoted by PostgreSQL's own
  `format()` rather than string concatenation, the rendered statement is never
  included in an error, and the supplied password is redacted from any error it
  does produce ([#26]). **Clever Cloud's managed PostgreSQL cannot create
  database users** (provider confirmation, 2026-08-28), so this is not the
  operator deployment's arrangement and nothing outside its own tests calls it;
  see the SPEC amendment under Changed.
- The migration ledger is covered by the same enforcement as the rest of the
  schema: it is created by the runner (so it can be read before deciding what
  to apply), its live shape is asserted against PostgreSQL's own catalog rather
  than the migration text, its recorded version persists across connections so
  a restarted instance reapplies nothing, and dropping it is a discrepancy
  `Verify` reports.
- Server-time fenced host leases and exactly-once publication: acquire, renew,
  and release with a monotonic per-host fence, and `PublishSnapshot`, which
  records a snapshot and its session rows under a lease it validates with a row
  lock both before writing and immediately before commit. A writer with a
  superseded fence, or whose lease expires mid-publication, lands nothing. A
  repeated idempotency key is a no-op, so a retried push after a lost response
  is safe. Session identity is an opaque digest over deployment, host, harness,
  and source id.
- Lease expiry is judged against `clock_timestamp()` rather than `now()`.
  PostgreSQL's `now()` is the transaction timestamp and is frozen for the whole
  transaction, so a lease validated inside a long publication could never
  observe an expiry that happened while that transaction ran, and the TTL
  bounded nothing.
- Reconciliation and catalog rebuild: the repository snapshot list is truth, so
  `Reconcile` adopts snapshots the catalog lacks as `catalog-pending`, never
  downgrades what a push already committed, and reports snapshots the
  repository no longer lists as an anomaly rather than deleting them (retention
  is append-only). `Rebuild` reconstructs a host from the listing alone, which
  is the recovery path for a lost Phase A database, and is deterministic so two
  instances recovering independently agree on ordering.
- Snapshot attribution is checked rather than assumed: a listing that mixes
  hosts, names a different host, or contains a snapshot recorded without
  `--host` is refused before anything is written, and host ids are validated
  with the same rule `--host`, `BABEL_HOST_ID`, and `storage.json` enforce.
  Session rows cannot come from the snapshot list, so a rebuilt host has none
  until its owner pushes again.
- **Correction.** An earlier entry in this release claimed restic reports
  backup counts only on its backup message and not in `snapshots --json`. That
  was wrong: restic stores a summary in the snapshot record, so `files_new`,
  `files_changed`, `files_unmodified`, and `data_added` are available from the
  listing. The claim came from reading Babel's own wrapper struct, which did
  not parse the field, instead of restic's actual output. `Snapshots` now reads
  it, and reconciliation and rebuild record real counts instead of discarding
  recoverable truth.
- Unknown counts are stored as SQL NULL rather than zero. A snapshot whose
  restic record carries no summary has counts that are unknown, and writing
  zero would assert it backed up nothing; the owning host's next push replaces
  NULL with real values. Session count is nullable for the same reason -
  reconciliation cannot know it without reading the snapshot's file tree.
- `restic ls` is wrapped, so a snapshot's file tree can be enumerated from
  metadata alone without downloading contents - the primitive cross-host fetch
  needs.
- Archived-session identification: each source adapter can recognize its own
  sessions in a snapshot's file listing, assigning the same source identity a
  local scan would. Identification is a pure function of the listing - no
  filesystem, no downloads - which is what lets one machine enumerate another
  machine's archived sessions. Each adapter is proved equivalent to its own
  `Discover` over the shared fixtures, and a combined listing holding all three
  harnesses' trees is proved to partition cleanly, so no adapter can claim
  another's files.
- Blob and attachment attribution is deliberately not inferred from paths.
  Which content-addressed blobs a session references lives inside its primary
  log, and identification does not read logs, so a closure derived from the
  listing covers the primary log and path-attributable sibling artifacts only.
  Guessing would fabricate a closure a fetch could not honour.
- `sessions fetch --host ID` materializes a session archived by any host,
  resolving the selector inside that host's snapshot instead of against local
  sources. It addresses a session this machine never had, or one whose local
  files are gone, and restores it byte-exactly. Identification reads only the
  snapshot's file listing, so no transcript bytes are downloaded to find a
  session. Selecting a host with no snapshots names the hosts that do have
  them rather than falling back to another machine's.
- A leak-channel acceptance for the web shell (SPEC.md §548): a unique
  sentinel is planted inside transcript content and a second one as the
  repository credential, then every API route, both error paths, the 401, and
  the browser's first load are exercised and searched. The credential reaches
  no response body, header, or log line; transcript content is confined to the
  transcript endpoint and *required* to appear there, so the confinement check
  cannot pass vacuously; every `/api` response is `no-store`; the launch token
  reaches no log line; and no selector carries either sentinel. Each search
  was mutation-tested against a planted leak. Scope limit: it drives the
  server's HTTP surface, not a browser. The client is a hash router, so
  selectors are never transmitted in a URL at all, but they do enter the
  history entry, which is why sentinel-free selectors are what the history
  channel rests on — established there by reading the route table, and now
  enforced by the browser acceptance below ([#34]).
- A browser-driven leak acceptance for the channels Go cannot observe
  (SPEC.md §548). A real headless Chrome drives the served bundle against a
  synthetic corpus carrying a sentinel in a transcript and a sentinel as the
  repository password, and proves: the fragment token authenticates; a reload
  and a back/forward navigation stay authenticated; the transcript actually
  renders, so the page is not passing vacuously empty; no history entry or
  request URL holds either sentinel or the token; no `/api` response is served
  from the browser cache; and a context without the token is refused. A new
  `browser` CI job runs it, and the test hard-fails rather than skipping when
  CI has no Chrome, because a silent skip would retire the gate. The
  address-bar assertion is **non-discriminating**, and the reason was measured
  rather than guessed: two independent mechanisms keep the token out of
  history, the bootstrap's `replaceState` and App.tsx's catch-all
  `<Navigate to="/sessions" replace />`, which the unmatched `#token=…`
  fragment falls through to. Disabling either alone still passes; disabling
  both fails the history walk, which is bounded by the stack the browser
  reports and proves completion by landing on the context's initial blank
  entry, so a traversal that stops early — as it does when a retained token
  entry redirects away on arrival — is a failure rather than a silently checked
  prefix. Failure output reports the traversal with the token redacted. Both
  mechanisms are kept, since either alone is a single point of failure for a
  credential, and the route now says so where an editor would remove it
  ([#36]).
- **Two-instance acceptance now runs as a test** (SPEC.md §10's pre-deployment
  gate: a second independently configured instance must browse the shared
  catalog, fetch a session the first host archived, lose and rebuild its local
  SQLite cache, and recover cleanly). Two instances with their own HOME, XDG
  configuration/data/cache, host identity, and instance id share exactly what a
  real deployment shares — one restic repository and one PostgreSQL catalog —
  and the scenario proves, in order: both configure independently while only
  one migrates; host A publishes; host B browses the catalog it never wrote to;
  B publishes its own session and the catalog holds two hosts, ordered, with
  distinct session identities; B lists and then byte-exactly fetches A's
  session into B's own store, and A does the same for B's, since neither
  instance is privileged; B loses its local catalog and rebuilds it field for
  field, while the shared view and the already-materialized session — retained
  data, not cache — are untouched; a publication lease with a live owner defers
  the other claimant's push, leaving the snapshot durable and reported
  `uncatalogued`; and the next uncontended push adopts it with monotonic
  publication order.

  The catalog is reached through Babel's own configuration document, so the
  connection is TLS — shared mode cannot express `sslmode=disable`. A new
  `internal/pgtest` provisions throwaway clusters with a self-signed
  certificate for that reason, and its own tests assert the server's view of
  the connection (`pg_stat_ssl`) rather than trusting the client's request. A
  server without `ssl=on` refuses a `require` client outright — measured:
  `server does not support SSL, but SSL was required` — so a successful
  connection already proves encryption; what the server's view adds is the
  negotiated protocol, which is the same thing `storage verify` reports to an
  operator rather than echoing the mode that was asked for. `internal/sharedcatalog`'s harness now provisions through the
  same package.
- **`archive status` reports what the shared catalog holds, per host**, so a
  second instance can browse it rather than only compare totals against the
  repository. A second table lists each publishing host's catalog snapshot
  rows, distinct session identities, catalog-pending rows, newest publication
  order, and that row's snapshot time. It stays a separate table from the
  repository listing on purpose: the whole point of the column is that the
  repository and the catalog can disagree, and merging them would hide it.
  Absent in local mode, and absent rather than empty when the catalog could not
  be read.
- **`babel sessions list --host HOST [--snapshot ID]`** lists the sessions
  another host archived, which selective cross-host fetch needed to be usable:
  `fetch --host` already worked, but nothing could discover a selector to hand
  it. The listing reads only the snapshot's file tree — no transcript bytes are
  downloaded — so it reports harness, source id, selector, and primary size,
  and leaves title, workspace, modification time, and continuation grade
  absent, rendering `-`. Selectors are identical to the ones a local listing
  gives the same sessions, so an operator has one selector vocabulary whether
  the files are here or only in the archive. `--host` is rejected with
  `--roots` and `--no-cache`, and `--snapshot` without `--host`, each naming
  the conflict rather than silently preferring one source.
- **Direct recovery is now tested with `restic` alone** — no Babel in the
  restore path, no PostgreSQL, no configuration: just the repository locator
  and the password file, then a byte-for-byte comparison of every regular file
  under every backup root the push reported. This is the guarantee that makes
  the archive trustworthy independently of Babel (SPEC.md §14), and it was the
  one §14 leg with no coverage at all. Restoring through a Babel helper could
  have passed while the property failed, so the test shells out to the real
  binary. It walks the sources rather than the restore, since the reverse
  comparison would pass for a restore that dropped files, and it fails if the
  walk compared implausibly few files.
- **Two more §14 pre-deployment gates now have tests**, both reachable without
  the provider.

  *Idempotent concurrent writers*, proven with two operating-system processes
  overlapping in time rather than two sequential calls — it has to be out of
  process, since Babel resolves HOME and the XDG roots from the environment and
  that is process-global. Same host: a lease serializes writers rather than
  refusing them, so both committing is legal if the first released before the
  second asked; what must hold is that no push fails, every outcome is a state
  the catalog can be in, both snapshots reach the repository regardless of who
  won, and one later push settles whatever the race left. Different hosts: both
  commit, with separate leases and separate publication-order sequences. This
  test is what surfaced the repository-initialization hazard below.

  *Complete catalog rebuild from the repository snapshot list plus source
  rescans*, proven by destroying the catalog outright — `DROP SCHEMA babel
  CASCADE`, not a truncation — and recovering with only the documented path:
  migrate, then push. No catalog backup is assumed, because none is promised.
  What returns is snapshot visibility, ordering, restic's counts, and current
  session identity from the rescan; what does not is which sessions each
  *historical* snapshot held, so those rows come back `catalog-pending` and
  stay there. The test asserts that asymmetry rather than a total, which is the
  difference between checking a rebuild and checking a number.
- **`babel storage rebuild --host HOST --yes`** exposes the catalog rebuild
  SPEC.md §12 lists as a Phase A deliverable. `sharedcatalog.Rebuild` had
  implemented it correctly, with tests, since the schema landed — and no command
  could invoke it, so it was reachable only from its own unit tests. A
  deliverable nothing can reach is not delivered.

  Its doc comment also called it *"the recovery path that makes losing the Phase
  A database survivable"*, which the rebuild gate had just disproved by
  recovering without it: an empty catalog needs only `storage migrate` and each
  host's next push. Rebuild is the **repair** path, for rows that are present
  but wrong — which no push corrects, because a push appends its own snapshot
  rather than auditing the ones already recorded. The doc now says that.

  `--host` is required rather than defaulting to this machine, and `--yes` is
  required too, because the command discards derived rows and the wrong host
  would be a silent loss. An unknown host is refused naming the hosts that do
  exist, since a mistyped one would otherwise rebuild to empty. Ordering is
  rederived from restic's recorded times; session rows are discarded rather than
  invented, so the snapshots come back `catalog-pending` and identity returns
  with the owning host's next push.
- **Babel can reach an object store at all**, which it could not before. The
  restic child process gets a deliberately strict environment allowlist —
  `RESTIC_REPOSITORY`, `RESTIC_PASSWORD_FILE`, `RESTIC_CACHE_DIR`, `HOME`,
  `PATH`, `TMPDIR` — carrying no access key, and the storage document had no
  field for one. Every archive test ran against a local path, which needs no
  credential, which is exactly why nothing caught it. "Nothing has written a
  byte to Cellar" was not caution; it was impossible.

  `repository_store.access_key_id` and `repository_store.secret_access_key` now
  live inline in the document beside the catalog's password (decision 50). They
  are required for an `s3:` locator, refused in halves, and refused as an empty
  block, because deferring a credential error to the first backup is the worst
  moment to find it. They reach restic as `AWS_ACCESS_KEY_ID` and
  `AWS_SECRET_ACCESS_KEY`: restic offers no file reference for them the way it
  does for the repository password, so this is the one secret this path puts in
  a child environment, and the reason is recorded where the code does it.

  Proven against the real Clever Cloud Cellar add-on with synthetic fixtures:
  `archive init` created the repository, a push committed a snapshot and
  published its session rows to the real managed PostgreSQL, `verify` passed
  over S3, a second independently configured instance browsed the catalog and
  listed and fetched the first host's session, and **restic alone restored all
  five files byte-identically** with no Babel and no PostgreSQL involved.
  Neither credential nor the repository password appeared anywhere in the
  captured output.
- **The web lock/stop control**, the last named Phase A deliverable that was
  absent (§12's deliverable bullet, §2's contract, decisions 34 and 45). A
  same-origin `POST /api/lock` revokes the launch token **before** the listener
  closes, so a winding-down process cannot still honour it, and the page
  replaces its whole shell with a terminal state rather than appearing to work.
  `babel web` exits 0 when the operator asked it to stop.

  Implementing it surfaced that **`Host`/`Origin`/DNS-rebinding checks did not
  exist** — decision 34 requires them and the bearer token was carrying the
  whole CSRF defence alone. There is now one shared guard for every `/api` path,
  checked before the credential is read: `Host` must be the loopback literal,
  and `Origin`, when a browser sends it, must match. The token remains the
  primary defence; this closes the weaker signal that decision 34 names, and it
  matters most for lock/stop, where a forged request is a denial of service.

### Removed

- **`babel status` from §8's command list.** It appeared exactly once in the
  whole specification, with no behavioural rule, no §12 deliverable, and no
  acceptance text, at the tail of the Phase B commands. Bare `babel` is the
  offline status overview and has a rule, §8.1, and decision 12 behind it, so a
  second command with none of that was a list artifact rather than unbuilt work
  (operator decision 2026-08-29).

### Changed

- **The spec no longer assumes a pre-Babel backup job running behind Babel.**
  Six places did, including decision 4's "never deleted" clause and §12's
  rollback contract. Retiring that job is a per-machine dotfiles cutover and not
  something any Babel command does, so what §12 now records is the consequence
  that is Babel's: a deployment with no legacy job behind it has no second
  automated copy, and a generation from before the cutover reinstates an archiver
  Babel does not coordinate with (decision 52).

- **`archive push` no longer creates the repository; `babel archive init` does,
  once per deployment.** Auto-init on push was a data-loss hazard on the
  unattended path, found by writing the concurrent-writer test.

  restic generates a master key per `init` and writes the key before the
  config, so two inits racing on an empty repository **both succeed** and leave
  two valid keys with one config. restic then selects a key by iteration and
  fails outright when it picks the wrong one. Measured against restic 0.19.1:
  10 of 10 races left two keys, and 7 of 10 subsequent backups failed with
  `config or key <id> is damaged: ciphertext verification failed` — a
  repository needing manual repair. Two machines' hourly timers firing together
  at a new Cellar repository is exactly that race.

  The second hazard is worse: silent creation turned a **mistyped locator** into
  a brand-new empty archive. Hourly pushes would keep reporting success into it
  while the real archive appeared to stop growing — a failure that reports
  success, which is worse than one that stops.

  So `push` now calls `Require` and fails with `no repository at <locator>: run
  `babel archive init` once for this deployment`, leaving nothing behind. A
  repository that exists but does not open is reported as *that*, not as
  needing initialization, because initializing over a real repository whose
  password is wrong would answer a credential problem destructively.

  The existing `TestEnsureInitUnderConcurrentCallers` asserted this was safe
  and passed while the hazard was real: it checked that no error came back, not
  that the resulting repository was usable. It is replaced by a test of the
  property `push` now depends on — that a missing repository is distinguishable
  from every other failure.
- **The first deployment's catalog connection is encrypted but not
  authenticated, and SPEC.md now says so** (decision 48). Clever Cloud's
  managed PostgreSQL presents a self-signed certificate with **no
  subject-alternative name**, whose common name identifies a different
  instance than the one it serves — so `verify-full` cannot succeed there, and
  pinning the certificate would not supply the missing name. `require`
  negotiates TLS 1.3 and is the honest setting; the residual exposure is an
  attacker on the network path impersonating the database and capturing the
  catalog credential, bounded by Phase A sending only opaque identifiers,
  ordering, counts, commit state, and timestamps.

  Babel's own behaviour needed no change, which was worth confirming rather
  than assuming: its error names the real defect (`certificate is not valid
  for any names`), and `verify-full` was separately proven to **accept** a
  trusted chain with a matching hostname and to refuse on both CA and hostname
  grounds, so its rejections discriminate instead of being unconditional.
- `DetectPrivileges` no longer under-reports on a database Babel has never
  migrated. `current_schema()` resolves to nothing before the schema exists, so
  the schema-CREATE check reported `application` for a credential that can in
  fact set the deployment up; it now falls back to CREATE on the database,
  which is exactly the right required to create Babel's schema. This is the
  state `storage configure` runs in on a first deployment, and a real add-on
  reaches it.
- `AcquireHostLease` now refuses write authority against an incompatible
  schema rather than leaving the check unwired, so a binary cannot publish into
  a database migrated by a newer one. This is deliberately *not* downgrade
  protection: an older binary performs no version check, so nothing in Go
  constrains it — only what PostgreSQL evaluates for it, such as a lease's own
  SQL expiry predicate. The schema stays at version 1; an unmigrated database
  is named as such, with the command that fixes it, rather than surfacing
  whichever missing relation a query happened to hit first.
- Errors that carry a connection string now redact the password on its own as
  well as the whole DSN. A driver may reconstruct a connection string from
  parsed fields rather than echo the one it was given, and the whole-string
  replacement could not match that; pgx happens to omit the password when it
  does so, which made the guarantee depend on a dependency's discretion.
  Mutation-tested: both new arrangements survive redaction under the previous
  implementation.
- `storage verify` reported "pending migration: yes" on a catalog it had just
  finished migrating. It inferred pending-ness from the deployment's recorded
  `schema_version`, which answers a different question and is written at first
  publication rather than by migrating, so the two sources disagree in exactly
  the state a first-time operator sees. Pending migrations now come from the
  migration ledger, and a version of 0 renders as "not recorded yet" rather
  than as a bare zero beside a compatible schema. Found by driving the real
  binary against a live TLS-enabled PostgreSQL, not by reading the code.
- **The web launch token moved from the query string to the URL fragment**, as
  SPEC.md §146 always specified. The token now appears in exactly one place —
  the launch URL's fragment — and the bootstrap erases it from the address bar
  and the history entry on first load. Fragments are never transmitted, so it
  reaches no request line, access log, cache key, or `Referer`. A token
  supplied in a query string is refused rather than honoured, because
  accepting it would reopen every one of those channels. `Referrer-Policy:
  no-referrer` is now set on every response; CSP restricts load destinations
  and never governed the referrer. The nonce-to-cookie exchange §146 also
  describes remains unbuilt, and the bootstrap now documents its one implicit
  coupling: it must read the fragment before the hash router mounts, which ES
  module evaluation order guarantees. Verified in a real browser against a
  synthetic corpus: first load, reload, deep link, and back/forward all
  authenticate with the token absent from every transmitted URL and from the
  address bar after bootstrap, and a context without the token is refused
  ([#35]).
- **SPEC amended: shared mode's supported default is one database credential,
  not a role-separated pair.** A Clever Cloud employee confirmed their managed
  PostgreSQL cannot create database users, so the arrangement SPEC promised in
  eight places — a per-instance least-privilege application role plus a
  separate migration credential — is unavailable on the deployment provider.
  What the default gives up is recorded rather than softened: schema change is
  restrained by operator procedure instead of by privilege, and no
  database-level control can revoke a single instance, leaving fleet-wide
  rotation and repository-password custody as the honest remaining controls.
  Per-instance eviction is therefore absent rather than replaced. An
  application-level `revoke-instance` was built, measured, and **removed
  before shipping** (operator decision, 2026-08-29): revocation is ordinary DML
  on `instances` — a table every instance must already write to register
  itself — so any credential that can publish could revoke any instance and
  clear its own revocation, which a test demonstrated directly. A control whose
  authority cannot be authenticated reads as containment without being it, and
  the honest alternative is not to offer it. A retired machine's slot now frees
  when its lease expires. Whether per-instance eviction should exist at all,
  enforced by column-level grants and per-instance roles, is a §14
  pre-deployment decision and §12 Phase C work.
  Role separation stays specified and implemented for providers that
  permit it; granting least privilege to a provider-created user is explicitly
  untested on Clever Cloud and may not be relied on until proven against the
  real add-on (decision 46).

### Fixed

- **Stored timestamps did not sort in chronological order.**
  `time.RFC3339Nano` trims trailing zeros, so `12:00:00.1Z` compares its `Z`
  against the `2` of `12:00:00.12Z` and the earlier instant sorted second. Two
  frontier queries order by a timestamp column, so the defect was live, and it
  would only have appeared when two records landed within a tenth of a second
  — exactly when ordering matters. Timestamps now carry a fixed nine-digit
  fraction, which `RFC3339Nano` still parses, and a regression test asserts
  that text order equals time order and that every rendering is the same width.
  Found by a sibling agent hitting it as a real test failure in a package that
  reads these stores.
- **The end-to-end suite could have read the operator's real `storage.json`.**
  `newEnv` isolated HOME, `XDG_DATA_HOME` and `XDG_CACHE_HOME` but not
  `XDG_CONFIG_HOME`, and `os.UserConfigDir` prefers that variable over HOME. It
  was harmless only because no production configuration existed anywhere — and
  the very next step is creating one. A shared-mode document carries a real
  repository locator and a real catalog DSN, so any command resolving through
  configuration rather than explicit flags would have addressed the operator's
  actual Cellar bucket and PostgreSQL from a test run.

  `internal/cli`'s fixture already carried this guard, its comment recording
  that two unrelated tests once observed a configuration they never wrote; the
  e2e suite drives the same commands and lacked it. Reverting the one line makes
  the new regression test fail with `Fatal: /nonexistent/outside-password does
  not exist` while reaching for an `s3://outside.invalid/operator-bucket`
  locator, which is the hazard stated exactly.

- **A session's catalog identity was derived from `storage.json` rather than
  from the host actually publishing it.** The snapshot goes to restic under the
  resolved host — `--host`, else `$BABEL_HOST_ID`, else the configured value —
  and that resolved host takes the publication lease and owns the snapshot row,
  but the session-identity digest was computed from the configured `host_id`
  alone. Any override silently attributed a host's sessions to an identity that
  never published them, and two hosts archiving the same source tree collided
  on one digest instead of producing two, which is the uniqueness the digest
  exists to provide (decision 9). Session identity now follows the publishing
  host, and a test pushes one source session under two host identities and
  asserts the catalog holds two distinct identities — it holds one without the
  fix. The two-instance acceptance cannot catch this on its own, since its two
  hosts archive disjoint sessions.
- **The end-to-end suite flaked about one run in six, and the assertion was
  wrong rather than the code.** It appends to a session log and requires the
  next push to add fewer bytes than the file, which states deduplication. That
  only holds if the file spans more than one chunk, and the fixture was 4.34 MB
  against restic's 8 MiB maximum chunk size — so under some repositories the
  whole file was a single chunk and appending genuinely re-stored all of it.

  The randomness was not in the content, which is fixed-seed: **restic picks a
  chunker polynomial per repository**, and every run builds a fresh one.
  Measured across eight runs of byte-identical input, the second push added
  between 166 KB and 4.57 MB — and that upper figure exceeds the old fixture,
  which is exactly the observed failure. The padded log is now 11.12 MiB, above
  the 8 MiB bound, so at least two chunks exist under every polynomial and the
  assertion is an invariant instead of a coin flip. The test asserts that
  precondition itself, so shrinking the fixture fails loudly rather than
  quietly restoring the flake, and it logs the accounting so any future tighter
  bound can be argued from observation.
- A web-harness test waited exactly as long for graceful shutdown as the server
  gives itself (5s), so a correct-but-slow shutdown and the test's deadline
  raced; under full-suite load the test reported a hang that had not happened.
  The bound now exceeds the server's own budget.
- `--host` was bound by every repository-taking command, so
  `sessions fetch --host ID` was accepted and silently did nothing. It now
  selects the cross-host path; previously the flag parsed and was ignored.
- **Two browser leak assertions raced the page they were asserting about.**
  Back/forward and the unauthorized negative control waited on `location.hash`
  or a character count, both of which settle before the view behind them
  renders, so the content assertion could read `Loading session…` and fail
  roughly one run in six. Each step now waits for its destination's own
  content — or for an authorization failure, so a genuinely broken credential
  still fails fast and by name instead of timing out. Diagnosed from the
  captured failure rather than guessed, and clean across 22 subsequent runs
  ([#38]).

[#24]: https://github.com/atyrode/babel/pull/24
[#25]: https://github.com/atyrode/babel/pull/25
[#26]: https://github.com/atyrode/babel/pull/26
[#34]: https://github.com/atyrode/babel/pull/34
[#35]: https://github.com/atyrode/babel/pull/35
[#36]: https://github.com/atyrode/babel/pull/36
[#38]: https://github.com/atyrode/babel/pull/38

## [0.2.1] - 2026-08-28

Makes the first catalog scan observable, and stops it being needlessly slow.

### Added

- Determinate scan progress in the web UI: described/total with percentage,
  current harness, elapsed time, and rows-cached, with sessions appearing in
  the table as they are described so browsing can start immediately. An
  explicit empty state and error state replace the indefinite spinner, and a
  Refresh button reports its own in-flight state ([#12]).
- `GET /api/scan` and `POST /api/sessions/refresh`; `GET /api/sessions` now
  carries a `scan` object and returns cached rows immediately instead of
  blocking on a scan ([#12]).
- `sessions list` narrates cold runs on stderr (`describing 250/836 (codex)…`),
  throttled, with stdout still exactly one JSON document ([#12]).

### Fixed

- **A filtered listing wiped the rest of the catalog.** `sessions list
  --harness omp` deleted every cached Codex and Claude row, so the next full
  listing re-described the whole corpus. Pruning is now scoped to the
  harnesses a refresh actually covered, and an empty scope prunes nothing.
  Measured on an 836-session corpus: a warm unfiltered listing went from
  64.8s to 165ms ([#12]).
- **Cancelling a scan discarded all of its work.** Describes were committed
  in a single transaction at the end, so closing or reloading the page threw
  away everything described so far. Work is now committed in batches and a
  cancelled scan keeps what it finished, so scans resume instead of
  restarting ([#12]).
- Concurrent requests each started their own full scan; scans are now
  single-flight per data directory and run on a background context, so no
  HTTP request can cancel one ([#12]).
- The catalog is opened in WAL mode with a busy timeout, so readers see
  batches a running scan has already committed ([#12]).
- Frontend requests had no timeout and could spin indefinitely; every call
  now aborts after 20s and surfaces an error ([#12]).

[#12]: https://github.com/atyrode/babel/pull/12

## [0.2.0] - 2026-08-28

The web GUI becomes Babel's primary surface.

### Added

- `babel web`: the self-hosted loopback web GUI, now Babel's primary surface
  (operator decision 2026-08-28) — token-guarded 127.0.0.1 server with an
  embedded React app: session browsing with instant filter/sort, session
  detail with artifacts/blobs/completeness, a paginated transcript viewer
  with explicit raw degradation, and archive status/verify/fetch. The web
  API is served by the in-process headless CLI, so both surfaces share one
  implementation and one never-delete command set ([#10]).
- `babel storage configure --from-json FILE|-` and `babel storage status`:
  persistent repository configuration in `storage.json` (0600, atomic),
  resolved as flag > environment > storage.json ([#10]).
- SQLite session catalog cache: `sessions list` re-describes only new or
  changed sessions and drops vanished ones; `--no-cache` bypasses ([#10]).
- Bare `babel` is now a fast offline status overview (build identity,
  storage state, cached catalog size, web pointer) ([#10]).
- CI test gates (gofmt/vet/build/test with pinned restic) on every push and
  PR, tag-driven GitHub Releases with cross-platform binaries ([#1]), and a
  frontend typecheck/build job ([#10]).

[#1]: https://github.com/atyrode/babel/pull/1
[#10]: https://github.com/atyrode/babel/pull/10

## [0.1.0] - 2026-08-28

First working slice of Phase A: a headless CLI that archives all three
harnesses into a restic repository and retrieves any historical capture
byte-exactly. No TUI, no web UI, no PostgreSQL catalog, no persistent
storage configuration yet — repository selection is per-invocation
(`--repo`/`--password-file` or `$BABEL_RESTIC_REPO`/`$BABEL_RESTIC_PASSWORD_FILE`).

### Added

- Source adapters for OMP (with content-addressed blob closure), Codex
  (rollout logs plus the host-state session), and Claude Code, discovering
  and describing sessions in place with explicit completeness reasons
  (f2a1bf1, a64fc8e, 27ef1a6).
- Restic-backed archival core: idempotent repository init, per-host tagged
  snapshots, append-only retention (no `forget`/`prune` code path exists),
  structural and `--deep` verification, and snapshot-scoped restore (a879067).
- Headless CLI: `babel version`, `archive push|status|verify`, and
  `sessions list|inspect|fetch|prune --local`, all with `--json` contracts,
  terminal-safe output rendering, and distinct usage/failure exit codes
  (12a1dfa, a879067).
- End-to-end suite driving the real restic binary: three-harness round trip,
  old-generation retrieval by snapshot ID after an append, dedup bounds, and
  injected pack corruption caught by `verify --deep` (e3b987f).
- Audited product specification and architecture decisions in SPEC.md
  (86235db, ed0f82a, 5b8d593).

### Changed

- **Storage pivot (operator decision, 2026-08-27):** archival is delegated to
  restic; the bespoke content-addressed object contract, publication pipeline,
  and object-store backends were retired after being built and tested
  (ea65a45…85fe13f), replaced in 8636960 and a879067. SPEC.md and README.md
  rewritten around the restic model (5b8d593).

[Unreleased]: https://github.com/atyrode/babel/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/atyrode/babel/releases/tag/v0.4.0
[0.3.0]: https://github.com/atyrode/babel/releases/tag/v0.3.0
[0.2.2]: https://github.com/atyrode/babel/releases/tag/v0.2.2
[0.2.1]: https://github.com/atyrode/babel/releases/tag/v0.2.1
[0.2.0]: https://github.com/atyrode/babel/releases/tag/v0.2.0
[0.1.0]: https://github.com/atyrode/babel/releases/tag/v0.1.0
