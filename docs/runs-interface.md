# Runs interface: creating, watching and reading a run

Status: design, 2026-09-02. Companion to `docs/manifold-transition.md`, which
owns the transition sequence, the engine service seam and every manifold
prerequisite except the one this document files. Nothing here is built; every
claim about what exists today cites the file that proves it.

The operator's requirement, in intent: Babel is entirely runnable from its
interface, and runs are the crux — one interface dedicated to runs, from
creating a run through tracking it live (where it is, what it is doing, what
artifacts and data it is looking at) to its output, leveraging Babel's full
capabilities with complete traceability. Operator decision D4 (2026-09-02) is
settled: that interface is a fully plugin-rendered manifold UI. There is no
terminal fallback and no poll-grade compromise as the end state; a
plugin-declarable continuous stream channel is a manifold prerequisite, and
until it exists the interim is milestone events plus polled state, labelled
interim wherever it appears.

Every manifold capability named below carries one of three tags on first
appearance — `[exists]`, `[declared]`, `[absent]` — and a path, all at manifold
`dev` @ 68f102e, re-read for this document where the text depends on them.
`[declared]` means named in a ratified decision record and not in code.

Transition steps are numbered as `docs/manifold-transition.md` §5 numbers
them: step 1 (loading and the engine API) carries the run projection of §4;
step 4 (the runs console) carries the stream channel, the milestones, the
interim console in the shell and the first plugin surfaces. The issue drafts
are in `.omp/research/runs-issues.md`.

---

## 1. Purpose, and the invariant

The run is the unit of everything the operator sees. SPEC.md §2.6 makes Babel
the owner of the analysis control plane — job supervision, tool authorization,
output validation, receipts, cancellation of the whole process tree — and
states that analysis is never detached and that a model run starts only
through an explicit operator action unless scheduled inference was separately
enabled. SPEC.md §6.5 fixes what one run is: an operator's scope, one Code
profile, a capability grant, a worker whose every evidence request Babel
authorizes, a receipt that records policies, lineage, grants, traces,
deferred and rejected candidates, failures, resources and timing, and a
commit rule under which a run is globally committed only when its PostgreSQL
rows and encrypted objects have landed, and is otherwise visibly
`pending-sync`. SPEC.md §7 lists what every run records and why: reproducible
enough to inspect, not deterministic enough to promise identical ideas, with
review decisions surviving re-exploration.

The invariant this interface holds, stated so it can be checked:

> Nothing reachable through the runs interface lacks a `run_id`. Every
> hypothesis, observation, finding, proposal, objection, deferred or rejected
> candidate, disposition, tool decision, progress record, failure, presence
> row, spend figure and receipt revision is shown as belonging to exactly one
> run, and a run is reachable from each of them.

Three corollaries. A conductor cycle is a run and is shown as one, under the
authority the loop recorded (`internal/conductor/conductor.go:13-23`). A run
on another machine is a run: the interface is fleet-wide because Babel's
records, receipts and presence already are (`internal/presence/presence.go:1-11`).
And a run that failed, was cancelled, or has not synced is still a run with a
receipt, because the record of a failed run is exactly when the record is
needed (`internal/explore/explore.go:602-604`).

---

## 2. What a run is today

Re-verified in `internal/run`, `internal/explore`, `internal/worker`,
`internal/conductor`, `internal/presence` and `internal/web` on 2026-09-02;
`.omp/research/manifold-babelsurface.md` §2 is the inventory this section
confirms and extends.

### 2.1 One execution, fixed over a preparation, ending in receipts

A run is one execution of `babel explore --preparation ID`
(`internal/cli/explore.go:28-78`) or one conductor cycle reaching the same
code path (`internal/conductor/conductor.go:13-18`). It is fixed over an
immutable, content-addressed **Preparation** (`internal/run/preparation.go`)
and produces one **Receipt** per worker boundary it crossed: the exploration's
own, plus a separate receipt each for the challenger and the synthesizer when
they ran (`internal/explore/explore.go:614-620`), each amendable only by
appending a linked revision (`internal/run/receipt.go:561-584`). Resuming a
run is calling `Explore` again with the same `RunID`
(`internal/explore/explore.go:565-569`). There is no web route that starts,
polls or cancels a run, by a written decision
(`internal/web/analysis.go:21-47`).

### 2.2 Four vocabularies, none of them "run status"

Babel has no single run-status field. Four state machines each answer a
narrower question, and the projection in §4 carries all four verbatim rather
than collapsing them:

| Vocabulary | Values | Question it answers | Source |
| --- | --- | --- | --- |
| `explore.Stage` | `preflight`, `explore`, `challenge`, `synthesize` | which job of the run is executing or failed | `internal/explore/explore.go:92-97` |
| `presence.State` + `Freshness` | `running`, `finished`, `failed`, `cancelled`; `fresh`, `stale`, `lost`, `finished` | what a process last said about itself, and how much that claim is still worth | `internal/presence/presence.go:123-138, 298-315`; thresholds 30 s / 2 min / retention 24 h at `:321-342` |
| `run.Header.Sync` | `pending-sync`, `committed` | whether this receipt row reached the shared catalog | `internal/run/receipt.go:24-27` |
| `conductor.Outcome` | `running`, `ran`, `parked`, `idle`, `failed`, `interrupted` | what the loop decided about one cycle | `internal/conductor/conductor.go:64-84` |

Presence is advisory by construction: a missing heartbeat cannot distinguish
slow, disconnected, asleep and dead, so `Fleet()` classifies by heartbeat age
and hands back the age too, and nothing reaps a row
(`internal/presence/presence.go:33-49`).

### 2.3 Receipt shape

`run.Receipt = {Header, Preparation, Body}` (`internal/run/receipt.go:539-543`).

- **Header**, plaintext-eligible under SPEC.md §9's allowlist: `Schema`, `ID`,
  `RunID`, `PreparationID`, `Revision`, `Supersedes`, `RecordedAt`,
  `Authority`, `Sync`, `Counts` (`internal/run/receipt.go:460-477`).
  `Counts` is derived from the body — `ToolRequests`, `ToolsDenied`,
  `Retrieval`, `Deferred`, `Rejected`, `Failures`, `Redactions` — so a sealed
  body can still be listed, and a non-zero `Redactions` is itself the audit
  signal (`:436-449`).
- **Body**, sealed before it leaves the machine: `Cookbook []CookbookAsset`,
  `Frontier {Roots, Prior}`, `Capabilities {Sandbox, Tool, Repository,
  PublicResearch}` versions, `Job {Job, Prompt, Schema}`, `Policy {Redaction,
  Disclosure}`, `Worker *worker.Receipt` embedded whole, `Retrieval
  []RetrievalStep`, `Deferred`/`Rejected []Candidate`, `Failures []Failure`,
  `Resources` (pointer fields: absence is not zero), `Timing`,
  `AmendmentReason` (`internal/run/receipt.go:483-524`).
- **Worker receipt** (`internal/worker/receipt.go:28-113`): `JobID`, `RunID`,
  `Profile`, `Recipes`, `Sources`, `Worker` identity, `ProtocolVersion`,
  `Grant {Capabilities, Disclosure, ExpiresAt}`, `Privacy`, `Cost` (the
  profile's own estimate, never a measurement — `internal/worker/protocol.go:853-860`),
  `Containment`, `ResolvedCapabilities`, `Metadata`, `ToolRequests
  []ToolRecord`, **`Progress []ProgressRecord` and `ProgressDropped int`**,
  `Result`, `Failure`, `Resources` (last self-report), `UnknownFields`,
  `StderrTail`, `ExitCode`, `StartedAt`, `FinishedAt`, `Duration`.
  - The progress trail is bounded: `Limits.MaxProgressRecords`, default 256
    (`internal/worker/worker.go:33, 201-204, 236-238`); past the bound
    `ProgressDropped` is incremented instead of appending
    (`internal/worker/worker.go:839-843`). Each `ProgressRecord` is `{Seq,
    Stage, Message, Fraction, At}` (`internal/worker/receipt.go:139-145`),
    stage and message scrubbed of credentials before recording.
  - A `ToolRecord` is `{Index, RequestID, Capability, Tool, ArgumentsDigest,
    ArgumentsBytes, Allowed, DenyCode, Reason, At, Decided}`; arguments are
    absent by design, digest and size only
    (`internal/worker/receipt.go:115-136`). Capabilities are `corpus-search`,
    `repo-read`, `sandbox-exec`, `public-research`
    (`internal/worker/protocol.go:462-467`). The `tool-decision` `results`
    payload travels on the pipe and never into a receipt
    (`internal/worker/protocol.go:227-240`).

### 2.4 Authority record

`run.Authority{Kind, Ref}` with exactly three kinds — `operator`, `policy`,
`serendipity` — and a mandatory reference (`internal/run/receipt.go:66-78,
94-97, 112-128`). The references in use: `command:explore` for a typed
command (`internal/cli/explore.go:273`), `invitation:<id>` for the operator's
process-further queue drawn by the conductor's rung one
(`internal/conductor/ladder.go:194`), `duty:<name>` for a standing duty
(`internal/conductor/duty.go:35-38`, duties at `:16-30`), `draw:<id>` for the
serendipity floor (`internal/conductor/ladder.go:362`). The ladder's rungs are
`invitation`, `policy`, `serendipity` (`internal/conductor/ladder.go:20-33`);
the policy rung implements duties and states in its own depth note that the
spec's attention policy — lifecycle, focus, fleet — is not implemented in this
build (`internal/conductor/duty.go:214-221, 271`). A standing "watch" on a
record, entity or machine would be that unimplemented half; no Babel code
names one today. The same `Authority` struct is carried by
`presence.Announcement` (`internal/presence/presence.go:191-200`) and
`conductor.Cycle` (`internal/conductor/conductor.go:97`), so "why did this
happen" is one vocabulary across receipt, journal and fleet row.

### 2.5 Journals

Three durable journals and one advisory table:

- `run_preparation` and `run_receipt` in `$XDG_DATA_HOME/babel/durable.db`,
  append-only by trigger, `sync_state CHECK IN ('pending-sync','committed')`,
  `UNIQUE(run_id, revision)`, `UNIQUE(supersedes)`
  (`internal/run/store.go:145-186`); listing is `Receipts(limit, offset)`,
  newest first, `MaxListLimit = 500` (`internal/run/store.go:700-704`).
- `sync_run`, `sync_record`, `sync_payload`, `sync_record_edge`,
  `sync_record_subject` in the same file: the publication bookkeeping a run's
  closure is declared into, staged inside the same transaction that made the
  receipt durable (`internal/sync/journal.go:117-160`;
  `internal/explore/explore.go:988-1036`).
- the conductor's cycle journal: `Cycle{Seq, StartedAt, FinishedAt, Outcome,
  Reason, Rung, Authority, Resumed, RunID, Invitation, Sessions, Recipes,
  Roots, Note, PreparationID, ReceiptID, Cost, Currency, Failures, Cancelled,
  PID}` (`internal/conductor/conductor.go:90-120`), written `running` before
  the run starts so a dead conductor leaves a trace.
- presence rows, PostgreSQL only and deliberately so
  (`internal/presence/presence.go:13-31`): `Row{ID, Host, Deployment, Kind,
  RunID, Recipe, PreparationID, Authority, State, StartedAt, HeartbeatAt,
  FinishedAt, ReceiptRecordID, HeartbeatAge, Freshness, Local}`
  (`:246-289`), announced by the run (`internal/explore/presence.go:28-46`)
  and finalized as `finished`, `failed` or `cancelled` from the run's own
  verdict (`:67-86`); `MaxFleetRows = 500` (`internal/presence/presence.go:347`).

Every frontier record carries the run that wrote it: `RunID` on hypotheses,
observations, findings, proposals and status transitions
(`internal/frontier/model.go:499, 551, 618, 718, 973`).

### 2.6 Worker protocol event kinds, and what reaches the web UI

Eleven message types over newline-delimited JSON on the child's stdio
(`internal/worker/protocol.go:15-29, 315-327`). Two in-process callbacks exist
so an interface can stay responsive: `Config.OnProgress(ProgressRecord)`
(`internal/worker/worker.go:297-301`) and, one layer up,
`Options.OnRecord(RecordEvent)` / `Options.OnProgress(Stage, ProgressRecord)`
(`internal/explore/explore.go:546-561, 598-599`). Today the only consumer of
both is the CLI's stderr narration, throttled to one line per second
(`internal/cli/explore.go:83, 625-648`).

| Message | Direction | Reaches the web UI today |
| --- | --- | --- |
| `hello`, `accept`, `refuse` | handshake | never |
| `job-preamble`, `job` | Babel → worker | never; `job` carries the broker token and is recorded nowhere |
| `configuration` | worker → Babel | never live; its `privacy`, `cost`, `containment`, `capabilities`, `metadata` land in the receipt body, which no route serves |
| `progress` | worker → Babel | never; stderr only; the trail lands in the receipt body |
| `tool-request` / `tool-decision` | both | never; `ToolRecord` lands in the receipt body |
| `result` | worker → Babel | indirectly: the durable records it validated into appear on Hypotheses, Findings and Review once written |
| `error` | worker → Babel | never live; becomes `Failure`/`FailureRecord` in the receipt body |

Precisely what the current web UI carries about a run, verified against the
route switch (`internal/web/server.go:323-561`): `GET /api/analysis/state`
returns `RunSummary` rows — the header only: `receipt_id`, `run_id`,
`preparation_id`, `revision`, `recorded_at`, `sync`, `counts`, `authority`,
`host`, `host_attributed` (`internal/web/types.go:330-361`) — and a
`worker.available: false` refusal (`internal/web/analysis.go:96-136`);
`GET /api/fleet/presence` returns presence rows with `kind`, `run_id`,
`recipe`, `preparation_id`, state, freshness and heartbeat age
(`internal/web/presence.go:93-102`); every record DTO carries its `run_id`
(`internal/web/analysis.go:173, 398, 417, 440, 654, 757, 769`) but no listing
filters by it. There is no `/api/run`, `/api/receipt` or `/api/conductor`
route. Therefore the following never reach the web UI: the whole receipt body
(cookbook set, frontier scope, capability/job/policy versions, the embedded
worker receipt including grant, privacy, cost, containment, tool records, the
progress trail and `ProgressDropped`, result, failure, resources, stderr tail,
exit code; the retrieval trace; deferred and rejected candidates; Babel-side
failures; timing); every live signal (progress, tool requests and decisions,
records as they are written); and the conductor journal (parked reasons, idle
cycles, per-day spend against the ceilings), which only `babel conductor
status` reads.

---

## 3. Requirements

Each requirement is numbered, states what the interface shows or does, and
ends with the observation that tests it. "Projection" means the
host-independent contract of §4; "surface" means the manifold-rendered UI of
§5. A requirement holds for both the plugin surfaces and the interim shell of
§6 unless it says otherwise.

### 3.1 Creating a run

- **R1 Scope.** A run is created over an existing preparation, or over a
  scope the interface prepares first — session selectors and adapter roots as
  `babel prepare` takes them — and the preparation identity is shown before
  the run starts, because what the run read is stated rather than implied
  (`internal/cli/explore.go:30-32`). *Test:* creating from a scope yields a
  `preparation_id` in the create result that `runsGet` reports on the run.
- **R2 Focus.** The create form takes frontier roots (`--root`, existing
  candidates) and prior hypotheses, and an empty root set is shown as "broad
  discovery", a different statement from roots nobody selected
  (`internal/run/receipt.go:215-226`). *Test:* a run created with no roots
  shows `frontier.roots: []` and the words "broad discovery" in its detail.
- **R3 Recipes.** Recipes are chosen from the cookbook listing already served
  (`RecipeSummary`: id, version, kind, title, default, scope, stages,
  capabilities — `internal/web/analysis.go:78-87`); naming none runs the
  default-enabled lenses, and naming one runs exactly it
  (`internal/cli/explore.go:57-59`). The recipe's declared capabilities are
  shown beside the grant so an operator sees what a recipe will ask for.
  *Test:* the receipt's `cookbook` list equals the chosen set with versions.
- **R4 Profile.** The Code profile reference is `ID` or `ID@REVISION`, default
  the stored one (`internal/cli/explore.go:72-73`); the interface never
  displays or edits provider configuration (SPEC.md §2.6). *Test:* the receipt's
  `worker.profile` equals the reference given.
- **R5 Grant.** The capability grant is explicit per run: the subset of
  `corpus-search`, `repo-read`, `sandbox-exec`, `public-research`, the
  disclosure class, and — for public research — the fixed source URLs, since
  the capability is not granted without them (`internal/cli/explore.go:47-53`).
  The resolved profile's disclosure class and redaction requirement are shown
  and consented to before any material is sent (SPEC.md §3, `Privacy` at
  `internal/worker/protocol.go:848-851`). *Test:* a run created without
  `public-research` URLs has no `public-research` in `worker.grant`; a
  hosted-disclosure run over a corpus with a secret finding is refused before
  launch with `redaction-required` (`internal/explore/explore.go:170-174`).
- **R6 Budget.** `develop`, `retrievals`, `fetches` (default 8) and the
  challenge/synthesize passes are set per run with the same meaning as the
  CLI (`internal/explore/explore.go:530-544`; `internal/cli/explore.go:60-71`).
  *Test:* the receipt's retrieval count never exceeds `retrievals`, and a run
  with `challenge: true` has a challenge receipt.
- **R7 Authority.** A run created from the interface carries
  `authority.kind = operator` — the only kind a person can mint — in one of
  two modes. *Run now* records the acting principal as the reference
  (`principal:<id>`, §4.4; in the interim shell, which knows no principal,
  `shell:<host_id>`, §6), never `command:explore`, so a receipt can say a
  person clicked rather than typed. *Invite* leaves an invitation on a record
  for the loop to draw, which is the conductor's rung one and yields
  `invitation:<id>` when drawn (`internal/conductor/ladder.go:21-23, 194`).
  Duties (`policy`) are authorized through the conductor's toggles and never
  minted per run; serendipity is the loop's own floor; a standing "watch" is
  not offered because no build implements it (§2.4). *Test:* the create
  form exposes exactly the two operator modes; the receipt header of a
  clicked run reads `operator` with a `principal:` reference, and of an
  invited one `operator` with an `invitation:` reference.
- **R8 Target machine.** The form names the enrolled machine that executes
  the run, defaulting to the hub's own babel instance, and refuses a machine
  whose presence shows no babel service or whose worker is unconfigured, with
  the reason (`analysis.json` is per machine —
  `.omp/research/manifold-babelsurface.md` §3 item 4). *Test:* creating for a
  machine with no worker returns a refusal naming the machine and the missing
  configuration, and no run request is recorded.
- **R9 Idempotent create.** Every create carries a client-minted
  `idempotency_key`; a repeated create with the same key returns the same
  `run_id` and records nothing new, and a create with a new key over the same
  scope is a new run, because re-running an unchanged scope is allowed and
  never presented as equivalence (SPEC.md §7). *Test:* two creates with one
  key yield one run; two keys yield two runs over one `preparation_id`.

### 3.2 The live view

- **R10 State.** The live view shows the four vocabularies of §2.2 as
  recorded, never a derived "status" alone: stage, presence state with
  freshness and heartbeat age ("last seen 7m ago; running or dead, this host
  cannot tell"), receipt sync, and the cycle outcome when the run is a cycle.
  *Test:* a run whose process was SIGKILLed shows `running` / `lost` after
  the threshold, not `failed`.
- **R11 Current step.** The latest `ProgressRecord` — stage, message,
  fraction, at — is shown with its sequence number, and a gap in sequence is
  shown as a gap. *Test:* the sequence shown equals the worker's `seq`.
- **R12 Progress trail.** The bounded trail is shown in order with
  `ProgressDropped` rendered as "N further progress events were counted, not
  kept" when non-zero (`internal/worker/receipt.go:75-79`). *Test:* a worker
  emitting 300 progress events yields 256 shown and "44 counted, not kept".
- **R13 Evidence being read.** Each retrieval step is shown as the receipt
  records it — index, tool, scope, query, time, hits with rank and locator,
  frontier record ids for a self-retrieval, and the research source for a
  brokered fetch (`internal/run/receipt.go:265-324, 333-365`) — with rank
  labelled "presentation
  order" and never a score (SPEC.md §5.4), and each locator opening the record
  it recovers. *Test:* clicking a hit opens the transcript at the locator's
  path, line and byte offset.
- **R14 Tool requests and broker decisions.** Every tool request is shown as
  it is decided: capability, tool, arguments digest and size, allowed or
  denied, deny code, reason, decision latency (`internal/worker/receipt.go:121-136`).
  Denials are counted prominently, since a denied count is what a reviewer
  looks for first (`:170-172`). *Test:* a request outside the grant appears
  as denied `not-granted` within one live refresh.
- **R15 Sandbox commands.** A `sandbox-exec` request is shown as a tool
  request (R14) and labelled as a sandbox command. The command text, exit
  status, output digest and diff SPEC.md §2.6 asks a receipt to record are not
  in today's `ToolRecord`; the interface shows the digest and says so rather
  than inventing the command. *Test:* a sandbox-exec row shows
  `arguments_digest`, `arguments_bytes` and the sentence "command text is
  recorded as a digest".
- **R16 Records as they are written.** Hypotheses, observations, findings,
  proposals and objections appear in the live view as `RecordEvent`s arrive
  (`internal/explore/explore.go:550-561`), marked `reused` when a resumed run
  recognized them. *Test:* a record's id is visible in the live view before
  the run ends.
- **R17 Spend so far.** The live view shows what Babel can honestly count:
  tool calls, retrievals, fetches against their budgets, the worker's last
  self-reported resources (`cpu_seconds`, `max_rss_bytes`,
  `sandbox_bytes_written`, `tool_calls` — `internal/worker/protocol.go:839-844`,
  absent when unreported), and the profile's cost estimate labelled
  "estimate, never a measurement" (`internal/worker/protocol.go:853-860`). For
  a cycle, the day's spend against the ceilings and the unpriced count
  (`internal/conductor/budget.go:43-63`). *Test:* a worker that reports no
  resources shows "not reported", not zeros.
- **R18 Cancellation.** Cancel is available while presence is `running`,
  asks for a reason, and is shown as requested until the executing process
  acknowledges by finalizing presence as `cancelled`; everything committed
  before cancellation stays queryable (`internal/explore/explore.go:25-33`;
  `internal/explore/presence.go:71-77`). *Test:* cancelling a run mid-develop
  leaves its candidates on the frontier and its receipt records
  `cancelled`.
- **R19 Containment.** The worker's declared containment — backend,
  filesystem isolation, network default deny, resource ceilings, disposable,
  and the mandatory `escape` sentence — is shown from the moment the
  configuration event is recorded (`internal/worker/protocol.go:169-187`).
  *Test:* the `escape` text is visible on the live view and on the receipt.

### 3.3 The output view

- **R20 Records.** The output view lists every record the run wrote, grouped
  by kind, with current status, and links to the record pages: hypotheses,
  observations, findings, proposals, promoted, objections
  (`internal/explore/explore.go:622-633`). *Test:* the list equals the
  `Outcome` id lists for that run.
- **R21 Deferred and rejected candidates.** Both lists are shown with
  their reason and origin evidence when recorded
  (`internal/run/receipt.go:367-381`), because resource limits choose what is
  explored now, not what ideas may exist (SPEC.md §5.2). *Test:* the counts
  equal `counts.deferred` and `counts.rejected`.
- **R22 Dispositions.** Proposed next actions the run attached are listed
  with their ledger state, as proposals waiting for a click
  (`internal/explore/explore.go:635-638`). *Test:* accepting one from the
  output view appears in `/api/record/dispositions` for that record.
- **R23 Receipt.** Every receipt of the run — exploration, challenge,
  synthesis — and every revision is browsable section by section: header,
  cookbook, frontier, versions, worker, retrieval, tool requests, progress,
  deferred, rejected, failures, resources, timing, amendment reason. A body
  is rendered only where the reader holds the key ring that opens it, which
  every authorized host does (SPEC.md §9; `.omp/research/manifold-babelsurface.md`
  §3 item 16); a header whose body cannot be opened says so instead of
  rendering an empty section. *Test:* revision 2 shows its
  `amendment_reason` and links to revision 1.
- **R24 Failures.** Babel-side failures (stage, code, message, at —
  `internal/explore/explore.go:140-168`) and the worker's first failure
  (origin `worker` or `babel`, code, retryable —
  `internal/worker/receipt.go:158-168`) are shown separately, because a
  worker that reported its own failure behaved correctly. *Test:* a
  `development-path` refusal appears under Babel-side failures with stage
  `explore`.
- **R25 Duplicates.** Near-duplicate warnings the run recorded are shown
  beside the candidate they concern with the overlap and the record to
  compare against (`internal/explore/explore.go:645-650`). *Test:* each
  warning links two record pages.
- **R26 Preflight.** The deterministic preflight report — disclosure,
  redaction required, inputs, bytes, events, secret findings — is shown as
  the run's first section. Today the report reaches only the CLI's `--json`
  summary (`internal/cli/explore.go:93-105`) and the receipt keeps only a
  preflight-stage failure (`internal/explore/explore.go:141-142, 608-612`),
  so the projection persists the summary row beside the run's milestones
  (§4.5) rather than reading it from a receipt. *Test:* a run refused at
  preflight shows the report and no worker section.
- **R27 Export.** A run's receipts export as JSON exactly as stored, through
  the same export path records use (`GET /api/export` today). *Test:* the
  exported header round-trips through `run.Header` unchanged.

### 3.4 Traceability

- **R28 Run ↔ preparation.** A run links to its preparation and a
  preparation lists every run over it. *Test:* two runs over one preparation
  appear on that preparation's page.
- **R29 Run ↔ receipt ↔ revisions.** A run lists its receipts by stage and
  revision, and each receipt names its run. *Test:* following `supersedes`
  from the newest revision reaches revision 1.
- **R30 Run ↔ records ↔ dispositions.** Every record page names its run and
  links back; the run's output view lists the record; dispositions and
  review decisions on that record are reachable from the run. *Test:* from a
  finding page, the run page, and from the run page, the finding's review
  history.
- **R31 Run ↔ presence.** A running or recent run links to its presence row
  and the row to the run. *Test:* the fleet view's row for a run opens that
  run.
- **R32 Run ↔ manifold trace ledger and acting principal.** A run created,
  cancelled or reported through a manifold action door leaves a trace row —
  `ts`, `principal_id`, `authority`, `door`, `targets`, `payload` (redacted,
  4 KiB), `outcome`, `session` — in manifold's `events` table
  (`[exists]`, ADR 0018 §2, `docs/decisions/0018-trace-ledger.md:66-80, 149-162`;
  `packages/protocol/src/trace.ts:33-43`). The run records the acting
  principal as its authority reference (`principal:<id>`), and the run page
  shows door, principal and time for each of its dispatches. Correlation is by
  `(door, principal_id, ts)` because `ActionCtx` carries no trace id
  (`[absent]`: the ladder mints `traceId` at
  `packages/server/src/plugin-host.ts:1172` and the context at `:249-350` does
  not carry it; the general-improvement draft "Hand an action handler its
  trace id" (atyrode/manifold#166) is `docs/manifold-transition.md`'s). Reading the ledger is
  root-only through `core.events.list` (`[exists]`, limit 500, filters
  `kind` and `containerId` only — no door or principal filter —
  `packages/plugins/events/src/index.ts:52-53, 139-151`), so the run page's
  dispatch list is Babel's own record and the ledger is the cross-check.
  *Test:* a clicked run's receipt reads `operator` / `principal:<id>`, and
  the `core.events.list {kind: "trace"}` rows of that minute, filtered by
  the caller on `door = babel.<x>.runsCreate`, contain one with that
  `principal_id` and outcome `ok`.
- **R33 Everything is addressable.** Every run, receipt and receipt section
  has a stable address the interface can navigate to and share. A run is not
  a manifold node — the address grammar has seven kinds and none is a run
  (`[exists]`, `packages/protocol/src/uri.ts:33-45`) — so the address is a
  plugin route path (`[exists]`, `contributes.routes`,
  `packages/protocol/src/plugin.ts:277-301`; `packages/web/src/app.tsx:41-76,
  176-215`) of the form `/babel/runs/<run_id>[/receipt/<receipt_id>[/<section>]]`,
  and `host.navigate(path)` (`[exists]`, `docs/PLUGINS.md:774`) reaches it.
  The consequence is stated rather than hidden: an individual run cannot be
  named by a grant row or subscribed to as a topic, because both take a
  `manifold://` node (`packages/protocol/src/grants.ts:46-51`;
  `packages/protocol/src/events.ts:109-117`); the plugin's collection topic
  and the console container of §5.2 are the nodes that stand for all runs.
  *Test:* pasting a run path into the browser opens that run.

### 3.5 Conductor cycles as runs

- **R34 A cycle is a run.** Every cycle appears in the runs list with its
  rung and authority (`invitation:`, `duty:`, `draw:`), the loop's note ("what
  the loop actually decided"), and its outcome; `parked` and `idle` cycles
  appear too, with the reason, since an instrument with nothing to look at
  should say so (`internal/conductor/conductor.go:73-77, 86-120`). *Test:*
  a parked cycle shows the budget sentence `refuse` produced
  (`internal/conductor/budget.go:72-81`).
- **R35 Resumption.** A cycle marked `resumed` links to the interrupted cycle
  it continues and to the one run identity both share
  (`internal/conductor/conductor.go:98-101`). *Test:* the two cycles show one
  `run_id`.
- **R36 Ceilings.** The runs list shows the day's spend, the ceilings, the
  currency and the unpriced count next to the cycles, read from receipts
  rather than the loop's memory (`internal/conductor/budget.go:99-118`).
  *Test:* a hand-started run counts toward the day's spend.

### 3.6 Fleet-wide runs

- **R37 Every machine.** The runs list covers every host the shared catalog
  knows, attributing each run to its host and marking unattributed runs as
  such rather than as this machine (`internal/web/types.go:349-361`); filters
  are host, phase, authority kind, and time. *Test:* a run committed on host
  A is listed on host B with `host: A`.
- **R38 In flight elsewhere.** Runs in flight on other machines appear from
  their presence rows with freshness and heartbeat age, and their live
  progress from the presence heartbeat snapshot (§4.5, interim) or the stream
  (§5.3, end state). *Test:* a run started on a spoke shows `running` /
  `fresh` on the hub within one heartbeat interval.
- **R39 Machine identity.** The host shown is the catalog's `host_id` with
  its display name, the same identity presence and snapshots carry
  (`internal/presence/presence.go:249-253`); under manifold it is also the
  enrolled machine's name and online state (`[exists]`,
  `core.machines.list`, `packages/plugins/machines/src/index.ts`, cited by
  `.omp/research/manifold-machines.md` fact 21). *Test:* a host whose babel
  service is present but whose manifold agent is offline shows both facts.

### 3.7 Failure and pending-sync states

- **R40 Failed is not lost.** A run whose worker failed, whose stage was
  refused, or whose preflight refused launch shows as `failed` with its
  receipt and failure sections; it is never hidden from the list. *Test:*
  a `no-recipe` refusal is listed and opens.
- **R41 Pending-sync is visible.** A receipt whose `sync` is `pending-sync`
  is marked so on every surface it appears on, with the reason the catalog
  gave when one exists, and is not presented as globally reviewable
  (SPEC.md §9, `internal/explore/explore.go:1011-1019`). *Test:* a run
  completed during a catalog outage shows `pending-sync` until `babel sync`
  commits it, then `committed`.
- **R42 Lost is not failed.** A presence row past `LostAfter` is shown as
  `lost` with its age, never as `failed`, and a receipt arriving later
  resolves it (`internal/presence/presence.go:306-310`). *Test:* a run killed
  by an agent restart shows `lost`, then `interrupted` once a conductor
  records it.
- **R43 Sync-degraded reads.** When the shared catalog is unreachable the
  list is served from local state and says so, in the same `sync_degraded` /
  `sync_detail` shape the shell already uses (`internal/web/fleet.go:386-389`).
  *Test:* with PostgreSQL unreachable the list still shows local runs and
  the degraded notice.

### 3.8 Hostile-content rendering

Transcripts, worker messages, tool reasons, recipe ids chosen on other
machines, and every string a model produced are untrusted (SPEC.md §2.7,
§9). Sanitization is Babel's own job: manifold ships no sanitizer
(`[absent]`, re-verified 2026-09-02 — `sanitiz|DOMPurify|dompurify|XSS|innerHTML|dangerouslySetInnerHTML`
matches nothing under `/home/alex/manifold/packages`; `.omp/research/manifold-webshell.md`
fact 21).

- **R44 One sanitizer at the projection boundary.** Every string the
  projection returns has passed the response sanitizer the shell already
  applies — C0/C1 controls, DEL, invalid UTF-8 and unsafe runes rendered as
  visible escapes (`internal/web/server.go:914-953, 993-1032`) — before any
  host renders it. *Test:* the hostile fixture of `internal/web/phaseb_test.go:1252-1258`
  driven through every projection method yields no raw `\x1b`, `\u202e`,
  `\u200b` or `<script` on the wire and no emptied field
  (`internal/web/presence_test.go:521-549` is the shape).
- **R45 Text is text.** Worker messages, reasons, notes and record prose are
  rendered as text nodes; no surface uses raw HTML insertion. Under ADR 0016
  `[declared]` an isolated plugin has no DOM and its UI is a serialized
  component tree the engine renders (`docs/decisions/0016-plugin-isolation.md:219-231`),
  which makes R45 structural rather than disciplinary. *Test:* the `<script`
  fixture renders as the literal characters.
- **R46 Markdown and diffs.** Where a run's output is rendered richly
  (proposal bodies, diffs), the renderer works from an allowlisted AST with
  raw HTML, active SVG, scriptable URLs and unsafe schemes removed (SPEC.md
  §2.7), and under isolation that AST is what becomes the component tree.
  *Test:* `javascript:` and `data:` links in a proposal render as inert text.
- **R47 Locators, not links.** A retrieval hit's locator is rendered as a
  navigation to Babel's own transcript view, never as an external link, and
  a URL inside model text is never made clickable. *Test:* a hit whose
  excerpt contains a URL shows it as text.
- **R48 Bidi and invisibles.** Bidirectional overrides and zero-width
  characters are escaped so a reviewer's reading of a claim cannot be
  reversed (`internal/web/phaseb_test.go:1263-1267`). *Test:* the `\u202e`
  fixture renders as `\u{202e}`.

---

## 4. The host-independent run projection

The projection is the contract Babel exposes regardless of host: today
through `internal/web`'s routes to the React shell, tomorrow through the
engine service and its engine API (the seam `docs/manifold-transition.md`
owns; the Babel draft "Action-door API over the babel engine: the babel.*
action set and the headless engine service behind it" (#141) builds it), consumed by
the plugin's server half as one engine-API client. Every method below is one
JSON request and one JSON response, and every action in §4.6 is that method
with the identical input and result schema.

### 4.1 Vocabulary carried verbatim, plus one derived phase

Every run object carries `stage`, `presence {state, freshness,
heartbeat_age_seconds}`, `sync`, and `cycle {outcome, reason}` when it is a
cycle, exactly as §2.2 defines them. It also carries one derived `phase`, with
its derivation stated so a reader can check it:

| `phase` | Derivation |
| --- | --- |
| `requested` | a run request exists (§4.4) and no presence row or receipt yet |
| `running` | presence `running`, any freshness; `freshness` says how much to trust it |
| `finished` / `failed` / `cancelled` | presence terminal state, or, when presence never landed, the receipt: `failures` empty / non-empty / `cancelled` failure code |
| `interrupted` | a conductor cycle recorded `interrupted` for this run and no later attempt exists |
| `unobserved` | a receipt exists and no presence row was ever written (runs before issue #118, or a catalog that was unreachable at announce time) |

`phase` never overrides a source vocabulary; a surface shows both.

### 4.2 Bounds

The inbound action-body ceiling is 1 MiB (`[exists]`, `MAX_HTTP_BODY_BYTES`,
`packages/server/src/http.ts:36, 91, 103`); every session-socket frame is
bounded at 1 MiB (`[exists]`, `MAX_SESSION_FRAME_BYTES`,
`packages/protocol/src/elements.ts:8`); no outbound HTTP cap was found, and
the precedent for collections is paging (`core.events.list`, limit ≤ 500).
The projection therefore bounds every response to under 1 MiB by
construction: keyset cursors (`cursor` is opaque, encoding the sort key and
id of the last row), `limit` ≤ 100 for list methods and ≤ 500 for section
pages, strings truncated at a stated per-field bound with a `truncated: true`
marker, and no unbounded array anywhere. A caller that needs a whole receipt
walks its sections.

### 4.3 Read methods

- `runsList {host?, phase?, authority_kind?, kind?: "explore"|"conductor",
  since?, until?, cursor?, limit?}` → `{runs: RunSummary[], next_cursor?,
  sync_degraded?, sync_detail?}`. `RunSummary` is today's header row
  (`internal/web/types.go:330-361`) plus `phase`, `stage`, `presence`,
  `cycle`, `recipes: [{id, version}]` (primary first), `started_at`,
  `finished_at`, and `spend {estimate, currency, unpriced}`. Sorted by
  `started_at` descending, then `run_id`.
- `runsGet {run_id}` → `RunDetail`: the summary, `preparation {id,
  prepared_at, source_count, sync}`, `authority {kind, ref, principal_id?}`,
  `receipts: [{stage, receipt_id, revision, supersedes?, recorded_at, sync,
  counts}]`, `counts` (sum over stages), `worker {profile, identity,
  protocol_version, grant, privacy, cost, containment, resolved_capabilities}`
  from the exploration receipt, `budget`, `timing`, `preflight {version,
  disclosure, redaction_required, inputs, bytes, events, secret_findings,
  findings}` (R26), `dispatches: [{door, principal_id, at, outcome}]`
  (§4.4), and `cancel {requested_at, requested_by, reason,
  acknowledged_at?}` when a cancel exists.
- `runsSection {receipt_id, section, cursor?, limit?}` → `{section, items:
  [...], next_cursor?, dropped?: int}` for `section ∈ cookbook | frontier |
  versions | worker | retrieval | tool_requests | progress | deferred |
  rejected | failures | resources | timing | result | stderr_tail`.
  `progress` reports `dropped = ProgressDropped`; `retrieval` items carry the
  step's tool, scope, query, time, hits with `rank` and `locator`, `records`
  and `research`; `result`
  returns the validated payload paged by top-level key when it exceeds the
  page bound.
- `runsRecords {run_id, kind?, cursor?, limit?}` → `{records: [{kind, id,
  status, created_at, title_or_statement, reused, dispositions: [{id,
  state}]}], next_cursor?}` read from the frontier by `RunID`
  (`internal/frontier/model.go:499-973`).
- `runsLive {run_id}` → `{phase, stage, presence, progress_latest {seq,
  stage, message, fraction, at}, progress_seq_max, resources?, tool_requests
  {count, denied, last: [ToolRecord ≤ 20]}, retrievals {count, budget},
  fetches {count, budget}, records {hypotheses, observations, findings,
  proposals, objections, reused}, spend, cancel?, as_of}`. `as_of` is the
  instant the answer was assembled; a surface shows its age.
- `runsCycles {host?, cursor?, limit?}` → the conductor journal rows
  (`internal/conductor/conductor.go:90-120`) with the day's `spend`,
  `ceilings` and `unpriced` (`internal/conductor/budget.go:18-63`), for R34–R36.

### 4.4 Write methods

- `runsCreate {idempotency_key, target_host, preparation_id | scope
  {sessions[], roots[]}, recipes[], profile?, grant {capabilities[],
  disclosure, public_research_urls[]}, budget {develop, retrievals, fetches},
  challenge, synthesize, roots[], prior[], authority {mode: "now" | "invite",
  principal_id, record?}}` → `{run_id,
  preparation_id?, request_id, created: bool}`. `created` is false on a
  replayed key. The method records a durable **run request** — `request_id`,
  the full parameter document, `target_host`, `requested_by`, `requested_at`,
  `idempotency_key` — in the shared catalog, or in the local journal when the
  deployment runs in local mode and the target is this machine, then binds
  execution:
  - on the hub, the engine service launches the run in-process through
    `explore.Controller` (`internal/explore/explore.go:359-375`) exactly as
    `babel explore` does, so the receipt is written by the same code;
  - on a spoke, until manifold has a typed exec/job primitive on the machine
    channel (`[absent]`, machine channel verbs are PTY-only,
    `packages/protocol/src/machine.ts:36-93`; the prerequisite is the manifold
    draft "Typed exec/job primitive on the machine channel" (atyrode/manifold#156)), the OS-supervised
    babel service on that host draws the request as work of the operator's
    rung — the same rung invitations use (`internal/conductor/ladder.go:21-23`),
    subject to that host's ceilings, which park it with the reason (R34) —
    and the request's `authority` is what the receipt records; this is the
    interim, and it is labelled so on the surface ("queued for <host>").
  The acting principal is recorded twice: on the request (`requested_by`) and
  on the receipt as `authority.ref = principal:<id>`, which is inside the
  plaintext allowlist because it is an identifier (`internal/run/receipt.go:80-88`).
- `runsCancel {run_id, reason}` → `{accepted: bool, phase}`. Records a
  durable cancel request; the executing process observes it at every progress
  event and at least every five seconds, cancels the run's context (which
  kills the process tree and keeps everything committed —
  `internal/worker/worker.go:435-438, 1532-1540`; `internal/explore/explore.go:25-33`),
  and finalizes presence as `cancelled`. `accepted` is false with the phase
  when the run is already terminal.
- `runsReport {run_id, milestone, at, payload}` — called only by the babel
  service executing a run, as the agent-kind principal D2 gives it, to post
  one §4.5 milestone. It is a write because under manifold it is the dispatch
  that emits (§4.6); host-independently it is how a spoke's run reaches the
  hub's projection before its receipt syncs.

### 4.5 Milestones and live state

Milestones are the bounded, discrete facts a run emits; they are not
progress ticks. Seven, each with a flat payload of at most the keys listed:

| Milestone | Payload keys | When |
| --- | --- | --- |
| `run_requested` | `run_id, request_id, host, authority_kind, principal_id` | a create was recorded |
| `run_started` | `run_id, host, stage, preparation_id, recipe, authority_kind, authority_ref` | presence announced |
| `run_stage_changed` | `run_id, stage, seq` | a stage began (≤ 4 per run) |
| `run_records_written` | `run_id, stage, hypotheses, observations, findings, proposals, objections, reused` | coalesced: at most once per 5 s per run, cumulative counts |
| `run_cancel_requested` | `run_id, principal_id, reason` | a cancel was recorded |
| `run_ended` | `run_id, state, stage, receipt_id, failures, tools_denied, redactions` | presence finalized; `state ∈ finished, failed, cancelled, interrupted` |
| `run_committed` | `run_id, receipt_id, revision` | the receipt's sync became `committed` |

Fine-grained live state — the latest progress record, resources, tool
counters — is not a milestone. Its carrier is, in the end state, the stream
channel of §5.3, and in the interim the presence heartbeat: the executing
process upserts a bounded **progress snapshot** (`seq, stage, message,
fraction, at, resources, tool_requests, tools_denied, retrievals, fetches`)
on its presence row at most once per second and at every heartbeat
(`HeartbeatInterval` 30 s, `internal/presence/presence.go:322-325`), and
`runsLive` reads it. Presence stays advisory and best-effort
(`internal/presence/presence.go:51-60`); a snapshot older than its heartbeat
is shown with its age.

### 4.6 Mapping onto manifold

Actions are the only mutation door and the only plugin-reachable HTTP
surface: `POST /api/actions/<manifest.id>.<name>` returning `{ok: true,
result} | {ok: false, denial: {rule, message}}` (`[exists]`,
`packages/plugin/src/action.ts:22-54`; `ActionOutcomeSchema`,
`packages/protocol/src/plugin.ts:761-765`; no route-contribution kind exists
— `[absent]`, `registerRoute|addRoute|http.route` matches nothing under
`packages/server/src` or `packages/plugin/src`, and `manifest.entry` is
"reserved, dynamic wave" at `packages/protocol/src/plugin.ts:554-555`). Reads
go through the same door, since a read is an exercise of authority too (ADR
0018 §Alternatives). Local names match `^[a-z][a-zA-Z0-9-]*$`, ≤ 32 chars
(`packages/protocol/src/plugin.ts:70-71`); with the Babel manifest id
`babel.<x>` the wire names are:

| Action | Caps demanded | Notes |
| --- | --- | --- |
| `babel.<x>.runsList` | `containers:read` | result ≤ 1 MiB, paged |
| `babel.<x>.runsGet` | `containers:read` | |
| `babel.<x>.runsSection` | `containers:read` | paged |
| `babel.<x>.runsRecords` | `containers:read` | paged |
| `babel.<x>.runsLive` | `containers:read` | polled in the interim |
| `babel.<x>.runsCycles` | `containers:read` | paged |
| `babel.<x>.runsCreate` | `containers:write` | emits `run_requested` |
| `babel.<x>.runsCancel` | `containers:write` | emits `run_cancel_requested` |
| `babel.<x>.runsReport` | `containers:write` | agent principal only; emits the reported milestone |

The capability vocabulary is a closed nine-member enum (`[exists]`,
`packages/protocol/src/capabilities.ts:11-25`), so Babel's own authority
domains are mapped onto `containers:read` / `containers:write` until manifold
offers more; the manifest's `capabilities` is a ceiling checked at assembly
(`packages/plugin/src/action.ts:12-14`). Every dispatch is a trace row
(R32).

The event plane (`[exists]`, ADR 0012, `docs/decisions/0012-event-plane.md:35-66,
109-210`): a plugin declares at most 16 event kinds
(`packages/protocol/src/plugin.ts:384-387`), each a global snake_case word ≤ 48
chars (`packages/protocol/src/events.ts:40, 59-60`); a payload is a flat
record of ≤ 16 keys, key ≤ 64 chars, scalar values with strings ≤ 20 000
chars or bounded arrays of scalars, no nesting
(`packages/protocol/src/events.ts:63-74`; `packages/protocol/src/elements.ts:4,
33, 36, 71-111`); emission is staged inside an action handler and flushes only
on `{ok: true}` (`packages/server/src/plugin-host.ts:335-349, 1207-1210`); an
undeclared kind is refused; delivery is to sockets subscribed at emission,
with no replay (`packages/protocol/src/session.ts:475-513`). The seven
milestones of §4.5 are the Babel plugin's declared `contributes.events`,
addressed to the collection topic `manifold://plugin/babel.<x>` because a
run has no node form (`docs/PLUGINS.md:966-971`), leaving nine kinds for the
rest of the plugin. Each milestone fits the payload bound as listed. The
executing babel service posts a milestone by dispatching `runsReport` as its
agent-kind principal; the handler stores it in the engine and emits it, so
the ledger records who reported and the socket learns something happened.

Subscription authority is the `containers:read` walk, discharged at subscribe
and at every delivery, and a collection topic requires an unscoped token
(`docs/decisions/0012-event-plane.md:174-183`), which is the token the plugin's
web half holds for an operator. There is no per-event backpressure guard on a
session socket (`.omp/research/manifold-machines.md` fact 39), one more
reason milestones are coalesced.

---

## 5. Manifold fit

Fit tags follow `.omp/research/manifold-webshell.md` §Babel area → manifold
surface: *native* (the idiom exists for this), *adaptable* (the idiom exists
and carries it with Babel-side work), *no idiom*.

### 5.1 Which surface carries each part

| Part of the runs interface | manifold surface | Fit | Citation |
| --- | --- | --- | --- |
| Runs list, filters, "this machine then everywhere" | sidebar section, `presentation: disclosure`, body in `ScrollRegion` | adaptable — no virtualized list (`[absent]`, webshell fact 5), so the list is paged, not scrolled whole | `[exists]` `contributes.sections`, `packages/protocol/src/plugin.ts:181-189`; `packages/plugin/src/ui/scroll-region.tsx` |
| Runs console: the run page's live view and output view | container discipline `babel-runs` (one console container per deployment, §5.2) with a `ContainerRenderer` keyed by discipline id, showing the run the route path names | adaptable — same shape as `core.canvas` / `core.compositions` | `[exists]` `contributes.disciplines` ≤ 4, `packages/protocol/src/plugin.ts:362`; `DisciplineDefSchema`, `packages/protocol/src/placement.ts:390-397`; registry `packages/plugin/src/projection.ts:281-282, 389-392` |
| Receipt sections, transcript-at-locator | the same renderer, section segments inside the run's route path | adaptable | as above; `host.navigate`, `docs/PLUGINS.md:774` |
| Create-run form, cancel form | door forms generated from the action's zod schema | adaptable, not reusable as-is: the generator lives in `core.debug` and a plugin cannot import another plugin, so Babel re-pins rjsf or hand-builds; the rjsf ADR names a second consumer as the trigger to promote it | `[exists]` `packages/plugins/debug/src/door-form.tsx`; `docs/decisions/2026-09-01-rjsf-door-forms.md:72-74`; `docs/PLUGINS.md` §1 dependency budget |
| Inspecting a run's dispatches, authority and trace | F10 inspector's authority and trace panels, on the action node | native for the action, no idiom for a run node (a run is not a manifold node, `packages/protocol/src/uri.ts:33-45`) | `[exists]` `packages/plugins/debug/src/inspector.tsx` |
| "Create run", "Cancel run", "Open run" | commands in the one cmdk menu, via declared actions and bindings | native | `[exists]` `docs/decisions/2026-09-01-cmdk-command-menu.md:51-52` |
| Deep links to runs, receipts, sections | `contributes.routes` segment `babel`, full-page takeover, plus `host.navigate` | native mechanism, unproven for content (only `core.uri` occupies it) | `[exists]` `packages/protocol/src/plugin.ts:277-301`; `packages/web/src/app.tsx:41-76, 176-215` |
| "Running on N machines" status line | sidebar `plain` row, status-line idiom | native | `[exists]` `packages/plugins/shell/src/status-row.tsx`; `docs/PLUGINS.md:840-843` |
| Failures, pending-sync, cancel acknowledged | the one notice stack, `sticky` for a persistent failure | native | `[exists]` `useNotice`, `packages/plugin/src/ui/notice.ts` (webshell fact 14) |
| Live progress | — | no idiom today; §5.3 | facts 17–18 of the webshell brief; `packages/protocol/src/session.ts:155-282, 355-557` |
| Machine online / offline beside a run's host | `core.machines.list` and the `machine_online` / `machine_offline` events | native | `[exists]` `packages/plugins/machines/src/index.ts:56-60` |

Under D1 the Babel plugin loads out-of-tree, which means under ADR 0016
(`[declared]`, `docs/decisions/0016-plugin-isolation.md:178-240`) its web
half runs in a Worker with no DOM, no `@manifold/plugin` React interfaces, no
`usePolledResource`, and a UI that is a serialized component tree in a
closed, host-owned vocabulary. Every surface in the table above is therefore
specified as data — a projection object mapped to a component tree — and the
current React shell is the first renderer of that mapping. The manifold draft
"Size the isolated-plugin component vocabulary against a real plugin surface
inventory" (atyrode/manifold#160) (`docs/manifold-transition.md`) carries this table as part of that
inventory: a paged list, a sectioned document, a form, a status line, a
notice, a text-with-escapes leaf, an allowlisted-AST prose leaf, and a
locator link.

### 5.2 Container discipline, precisely

The runs console is one container per deployment, of discipline
`babel-runs`, created once by the engine through the index's own door
(`ctx.store.createContainer` for an in-realm plugin, `docs/PLUGINS.md:949-953`;
under ADR 0016 §2 a declared store slice, `docs/decisions/0016-plugin-isolation.md:212-214`)
and addressed as `manifold://container/<id>`. Its renderer lists runs and
shows the run the route path names, and the route renderer of R33 mounts the
same run view, so a deep link and an in-workspace tile show one component
tree. One container rather than one per run is a deliberate trade: a
container per run would give each run a node the permission waterfall could
scope (`[exists]`, ADR 0011; principal forms at
`packages/protocol/src/grants.ts:46-51`) and a socket could subscribe to
(`packages/protocol/src/events.ts:109-117`), but every container is a row of
the index section, which has no virtualized list (`[absent]`, webshell fact
5), and a deployment's runs are counted in hundreds. The console container is
therefore the node grants and subscriptions name; per-run authority waits on
a run-shaped node in the address grammar, which is a manifold question
`docs/manifold-transition.md` owns. The discipline's placement traits admit
no drops (`accepts: []`), since nothing is placed into the console.

### 5.3 The live-progress channel

End state, settled by D4: a plugin-declarable continuous stream channel on
the session socket, carrying Babel's progress records, tool decisions and
record events as they happen, at the cadence the worker produces them. It is
`[absent]`: the session channel's frame bodies are two hand-written closed
unions, `CLIENT_BODIES` and `SERVER_BODIES`
(`packages/protocol/src/session.ts:155-282, 355-557`), whose one continuous
occupant is terminal output (`terminal_output {terminalId, seq, data:
base64}`, `:421-426`, fed by the machine channel's `output` frame,
`packages/protocol/src/machine.ts:48-53`); the manifest's `contributes` has
no stream kind (`packages/protocol/src/plugin.ts:316-403`); and the event
plane rules continuous traffic out by design
(`docs/decisions/0012-event-plane.md:13-18`; `docs/PLUGINS.md:926-929`). The
prerequisite is filed as the manifold draft "Plugin-declarable continuous
stream channel on the session socket" (atyrode/manifold#169) (`.omp/research/runs-issues.md`).

Interim, labelled interim on every surface that uses it: the seven milestones
of §4.5 over the event plane, plus `runsLive` re-read on each milestone and
otherwise polled. For an in-realm plugin the read is `usePolledResource`
(`[exists]`, `packages/plugin/src/polled-resource.ts`; re-read on matching
event, `FALLBACK_POLL_MS = 2000` only while the socket is down, `:77,
274-277`). For an isolated plugin, which ADR 0016 §3 denies that hook, the
web half subscribes through its own client (`docs/decisions/0016-plugin-isolation.md:216-217`)
and re-reads `runsLive` through the door on a bounded cadence; the surface
shows `as_of` age so the operator knows the view is polled. No terminal
emulator carries progress in either state.

---

## 6. Interim: what the current shell carries over, and what it must not grow

The React shell (`web/src/pages`, `internal/web`) stays until the plugin is
proven. Three things are built in it now so nothing is thrown away when it
goes:

1. **The projection.** `internal/web` gains the §4 methods as routes —
   `GET /api/runs`, `GET /api/run`, `GET /api/run/section`,
   `GET /api/run/records`, `GET /api/run/live`, `GET /api/runs/cycles`,
   `POST /api/runs/create`, `POST /api/runs/cancel` — implemented over
   `run.Store`, the frontier, the conductor journal and presence, with the
   same input and result shapes the actions will carry. Create and cancel in
   the shell record the durable request and nothing more: the executing
   process is the OS-supervised babel service on this host drawing the
   request (§4.4's spoke binding, applied to every host until the engine
   service exists), never the `babel web` process, so lock-and-stop cannot
   orphan a worker — which was the first of `exploreRefusal`'s three reasons
   (`internal/web/analysis.go:25-31`). The second, authority a browser must
   not hold, is met by the request document carrying the grant, the profile
   and the disclosure consent explicitly, exactly as the CLI's flags do; the
   third, no listing of runs in flight, is met by presence and the request
   journal. The shell knows no principal, so a shell-created run's authority
   reference is `shell:<host_id>` — the one non-principal operator reference
   the projection records, retired with the shell. The engine service
   inherits these application services and drops the static React serving
   (the seam `docs/manifold-transition.md` defines). `RunSummary` and
   `analysisState.runs` (`internal/web/analysis.go:52-59`) become a view of
   `runsList`.
2. **The milestones.** The seven milestone kinds and their payload shapes are
   defined in Babel and written durably by the executing process (a
   `run_milestone` journal beside the conductor's), so a milestone exists
   before manifold's event plane carries it; the plugin's `runsReport`
   handler forwards what the journal already says.
3. **The sanitizer.** `sanitizedJSON` (`internal/web/server.go:914-953`) stays
   the one boundary every projection string crosses; the allowlisted-AST
   prose renderer for R46 is written as AST → component tree so the React
   shell and the isolated plugin render the same tree.

The presence progress snapshot (§4.5) is written by the executing process
today, since presence is already fleet-wide and PostgreSQL-only, and
`GET /api/fleet/presence` shows it.

What must not be built in the shell anymore: no SSE, WebSocket or long-poll
route in `internal/web` (the shell would then be a second live channel beside
the one D4 requires of manifold, and the stated reason for refusing one —
lock-and-stop revokes the listener a run would outlive
(`internal/web/analysis.go:25-38`) — still holds for the shell's own session);
no in-process run launch from `babel web`, and no create path that bypasses
the run request; no terminal-styled progress view; no page-local
sub-navigation that assumes React Router, since the plugin's addresses are
route paths and container addresses (R33). `exploreRefusal`
(`internal/web/analysis.go:43-47`) is replaced by the create route's own
refusals — no babel service on this host, no worker configured, ceilings
unset — each naming what is missing, and `worker.available` reports the
service's presence rather than a standing `false`.
