# 2026-09-13: the drain that did not drain

The operator asked, at 10:29Z on 2026-09-13, for one thing: spend one Anthropic subscription's
remaining 7-day usage (84-86% used, resetting at 13:00Z; the account is called *the drain
account* here) on continuous Babel reviewing - as many reviews as the machine allows, duplicate
compute welcome, because the tokens were going to be lost at the reset anyway. Two hours and
fourteen minutes later the window had not moved a single percent: 50 assessments were recorded
for 70 runs that reached the model, the machine sat at load 42 on 12 cores with swap full, four
generations of ad-hoc loop scripts had been written, six Go fixes had been committed live, and
the operator stopped everything. The subscription reset with ~14% unused. This document is for
whoever runs the next drain, and for whoever builds the plugin's drain operation: it records what
happened minute by minute, what the orchestrator did wrong, what the product did wrong, and
which issue now owns each of those failures.

One sentence of conclusion, because the orchestrator got it wrong on the day and repeated the
wrong version to the operator: the window did not move because reviews were not being produced -
each draw read ~12 GB and spent 4-8 CPU-minutes preparing before its first model call, and model
processes existed for about 13 of the 134 minutes - not because reviews cannot drain a window.

Two conventions. Ids in the tables below (`O1`-`O14`, `F1`-`F23`, `G1`-`G10`) are the finding ids
that the index in *What changes* resolves to filed issues; `Part 1` and `3.0` in an evidence cell
mean the *Timeline* and *What the artefacts corrected* respectively. Evidence cells that cite
`A`/`B`/`C`/`D` numbers (`B16`, `C1`, `D11`, ...) refer to the forensic reads taken during the
post-mortem session over the run artefacts, the Go tree, the machine's unit files and the plugin
tree; the `file:line` citations beside them are the primary evidence and are what to check.

## Timeline

All times UTC, 2026-09-13. "Fan" = a shell loop of N concurrent `babel evaluate` draws.

| Time | Event | Source |
|---|---|---|
| 10:29 | Operator asks: drain the drain account with review runs only, before the reset - continuous reviewing, as many as the machine allows. | user message |
| 10:31-10:35 | Installed nix `babel` (build 2026-09-12) refuses the frontier: `frontier schema version 8 is newer than this build supports (7)`. Every store build present (09-10, 09-11, 09-12) refuses (5, 6, 7). A tree build `~/.local/bin/babel-main` from `main` @ c2f0a8e opens it. | bash output |
| 10:36 | `babel conductor status`: parked after 3 consecutive failed cycles; evaluation ladder 6022 never reviewed; coverage "the last durable coverage check inspected a different input set than this projection was built from". | conductor status |
| 10:38-10:42 | Profile was rev 3 (yesterday's worker, gpt-only, yesterday's burn). Ceremony driven over a pty (`babel analysis profile configure --worker <the drain account's worker>`; first attempt refused `--worker-arg babel` - the README's documented invocation is stale). Rev 4 minted: claude-only, smart, thinking high, fallback on, accounts panel: three enrolled accounts, the drain account alone enabled. Usage panel read: 7d 84%, resets 2h22m; fable tier blocked. | pty logs |
| 10:42-10:48 | Probe draw: `preparing 926/926`, reviewing at ~+3 min, recorded `evr_85c8994c…` (no judgement), published 6 records. Total 5m27s. The `preparing 1/926` wall was visible here and was not read as the bottleneck. | bg_26 |
| 10:48 | Fan A: `review-loop.sh`, 10 draws, deadline 13:13Z. | hub start |
| 10:49-11:02 | Fan A: "17 reviews" in 14 min by the loop's count - it counted every `rc=0` exit, including `drawn:false` (see 3.0: 50 assessments for the whole day) - alongside `rc=1` failures: `UNIQUE constraint failed: sessions.path`, `an observed environment belongs to an outcome claim`, `credential-shaped material is forbidden in the ledger`. | fan console; 3.0 |
| 11:02-11:07 | Root causes read: two indexers insert the same new session (indexer.go:233); evidence-role submit refused (model.go:953 vs review.go:572). Fixes written + tests. Broker down: `omp usage --json` fails (`OMP_AUTH_BROKER_ACCOUNT_POOL_FILE` points at a missing `/run/code/account-pool.json`); `atyrode-omp-auth-brokers.service` is `failed`. | bash |
| 11:09-11:22 | `review-governed.sh` written (window-roll stop, exhaustion hold/probe, later a usage clock) and proven against a fake babel. Meanwhile Fan A still running on the old binary. | eval proofs |
| 11:22 | Binary swapped (fixes 8f7e2cf). Fan A told to stop. | bash |
| 11:23-11:29 | Policy read via `babel web` + nonce bootstrap: eval-policy-6 batch 24 / lease 900 s / per-cycle 25. Log analysis: **89 `drawn:true` vs 687 `drawn:false`** ("cycle batch of 24 assignments is already claimed", "27 being reviewed now"). eval-policy-7 posted: batch 64, lease 1800, per-cycle 100. Governed fan B started (24 draws, retrievals 30). | curl, logs |
| 11:29-11:34 | Fan B: every draw `rc=1 … begin index transaction: database is locked (5) (SQLITE_BUSY)`; the governor's exhaustion classifier matched the text and **parked the fan**. Index busy timeout raised 60 s -> 600 s, `Open` no longer rebuilds on BUSY (d4caca0). Ceremony re-run twice: rev 5 (xhigh, advisor audit=opus:high), rev 6 (advisor review=sonnet:medium) per operator. | fan console |
| 11:40-11:49 | Fan restarted on d4caca0. `not drawn: cycle batch of 64 assignments is already claimed` - the 24+ draws killed at 11:29-11:40 hold 30-minute leases. Fix 22a2c9e: a preparation failure gives its claim back. Binary swapped. Meanwhile manifold#543 gated and opened, code#162 opened (divided attention while engines=0). | fan console, gh |
| 12:04-12:07 | Operator: "usage at 0%?" / "56 minutes". Discovery: **engines=0 the whole time**; 24 draws all `futex_do_wait` with `index.db` open; one 40-minute-old draw from Fan A (still alive, `review-loop.sh` never fully stopped) holds the index write lock; the 600 s timeout makes everyone wait. Killed it; all draws killed; timeout back to 60 s; heartbeat added to the governor. | /proc, ss |
| 12:07-12:12 | Fan of 12 restarted. `/proc/<pid>/fd`: every draw reading `-code/2026-09-07T20-29-44…jsonl` (**240 MB, still being written**, mtime moving) - each draw re-indexes it under the single write lock, serially. Fix b3d57c8: a changed session younger than 2 min is not re-indexed. | /proc |
| 12:12-12:17 | Fan restarted on b3d57c8; batch raised to 256 / lease 5200 (eval-policy-8) because my own kills had refilled the 64 slots. **12:17:37 engines=8, 12:17:52 engines=9 / omp=36** - first real model traffic since 11:02. Reviews 1-11 between 12:18 and 12:25. | pgrep |
| 12:18-12:21 | Fans C (6, retrievals 40) and B2 (8) added. PR babel#257 opened with the fixes; babel#256 (plugin) opened draft. | hub, gh |
| 12:22-12:28 | Rate collapses: draws=24, engines=0. Cause measured: **every draw runs `preparing 1/984 … 984/984`** - `fixScope` describes, digests and indexes the entire corpus (13,554 `preparing` lines in one fan log; ~12 GB read per draw, 4-8 CPU-min) before one model call; with 26 draws the box is CPU-bound (load 26-42). A digest cache (`session_digests`, salience export/merge) written and tested. | log grep, /proc/io |
| 12:28-12:41 | Binary swapped, every fan restarted (kills -> more ghosts). Cache warms 0 -> 984 rows over 8 min (each cold draw still reads everything once). Description cache added (12:41) because `describe` also reads the file. Fan D (10) added at 12:31; C and D stopped at 12:38 when the box was laggy (load 42, swap 15.9/16 GB). Hand draw: preparation now 36 s. | bash |
| 12:42-12:45 | Warm draws return in ~50 s with `not drawn: held by another worker until 14:08` - the coordinator keeps choosing subjects held by the ~70 ghost claims of the draws I killed, with the 5200 s lease I set. Selection does not skip held subjects. **Everything killed at 12:44:55.** | fan console |
| 12:45-12:46 | Found `assignmentID = digest(subject, role, contextVersion, policyVersion, ordinal)` (selection.go:977): a new policy version voids every ghost. eval-policy-9 (batch 64) then eval-policy-10 (batch 256, lease 5200) posted. Fan "final" (16) started. | bash |
| 12:46-12:48 | Six `babel explore` launched as a second lane: first attempt refused (`explore requires --preparation ID`), retried with the newest preparation (`prep-ca47b6…`, stale: "changed since the preparation was fixed" x2). **All six failed at launch** (`the Code analysis worker could not run this exploration`, explore-burn-*.log:5-6) and then timed out on every `babel sync` publish; I did not read their logs and reported engines "from explores and draws". | explore logs; 3.0 |
| 12:48-12:53 | engines 5 -> 12 -> 7 (all from the review fan); 8 reviews completed by 12:50; 21 established TLS sockets from omp to :443, 152.7 MB sent / 2.0 MB received across them. 5-hour window reads 4% (it already read 4% at 12:31 per the operator; my reader had returned `0 0` at 12:04 - a reader bug - and I wrongly reported "0% -> 4%"). 7-day window 86% throughout. I said tokens were "not determinable"; they are in every run receipt's `Usage` (3.0), unread. | ss, usage-window.py; 3.0 |
| 12:54 | Operator: stop. All fans, draws, explores, engines and the policy web server stopped. | hub stop |

Net result: ~50 reviews recorded across the two hours (22 assessments 10:40-11:35, 11 on the
12:12 fan, 8 on the 12:45 fan, plus fans in between), 16+8 votes, 13 contributions, 6 filing
acts; zero measurable movement of the 7-day window; the operator's subscription reset with ~14%
unused.

### What the artefacts corrected

- "17 reviews in 14 minutes" (10:49-11:02) counted `rc=0` exits; the gen-1 loop counted a
  `drawn:false` return as a review. `evaluation_attempt` for the whole day: **completed 50, exposed
  90, failed 18, skipped 113**; `evaluation_record` kind=assessment: **50**. Fifty real reviews in
  two hours and fourteen minutes.
- The six `babel explore` runs at 12:46 **all failed immediately**: `babel: the Code analysis
  worker could not run this exploration.` (explore-burn-1..6.log:5-6), then every `babel sync`
  publish they attempted timed out. I reported engines "from explores and draws" at 12:48; the
  engines were the review fan's only. I never read the explore logs.
- `babel sync` publication failed **throughout the day**: `sync: publish run …: object store PUT
  analysis/…: context deadline exceeded` on nearly every draw from 12:18 (1129.log:36-38,
  1140.log:15-18); every sampled receipt is `pending-sync`. Each draw publishes synchronously at
  the end; a slow object store taxed every review. Never noticed.
- The auth-broker outage did break runs, not only the governor: `worker: engine: code engine: the
  account snapshot is unavailable, so the run would launch with no account policy at all: Get
  ".../v1/snapshot": dial tcp 127.0.0.1:46171: connect: connection refused` followed by `engine
  did not become ready in time: engine closed its stdout before a ready frame, exit status -1`
  (1048.log:41608-41609).
- Tokens **are** recorded by the Go worker: `worker.Receipt.Usage{InputTokens, OutputTokens,
  ReasoningTokens, CacheReadTokens, CacheWriteTokens}` from the RPC `get_session_stats`
  (`internal/worker/domain.go:329-334`, `rpc.go:383-388`, `receipt.go:96`), stored in the run
  receipt's payload BLOB (`run_receipt.payload` -> `/worker/Usage`) and surfaced nowhere - not on
  stderr, not in `babel evaluate --json`, not in any CLI. My "tokens are not determinable" was a
  fourth false claim: they were in `durable.db` the whole time. Read after the fact, for
  2026-09-13: **70 of 85 receipts carry usage; 25,253,771 tokens - 19,200,461 cache reads,
  5,650,336 cache writes, 402,132 output, 842 uncached input; 335 messages, 388 tool calls;
  $54.97 at API list price.** Per review: 3-5 messages, 3-6 tool calls, 2.4k-5.5k output tokens,
  $0.50-0.77. Seventy runs paid at the model for fifty assessments: twenty (28%) spent and
  recorded nothing (the refused submissions, F8). `Usage` **is** populated on `code engine`
  (v0.19) runs; plan assumption A6 is verified.
- The evaluation backlog **grew** during the drain: `unreviewed` 6024 (10:48) -> 6038 (12:45).
  The `coverage` note on every draw ("the last durable coverage check inspected a different input
  set than this projection was built from") is a standing degraded state nobody acts on.
- The lease durations by policy version (from `evaluation_claim`): eval-policy-6 avg 16.4 min,
  policy-7 31.5 min, policy-8 and policy-10 **86.7 min flat**, expiring 14:12-14:20Z - 70-90
  minutes after the window this drain existed to beat. That is the arithmetic of O5.
- The 240 MB "live session" is `-code/2026-09-07T20-29-44…` (a Code session the operator has
  open) and the 35 MB one is `-babel/2026-09-10T14-46-23…` - **this harness session**. Babel's
  own operator transcript, still being written, invalidated every draw's corpus and every
  explore's preparation ("changed since the preparation was fixed" on all six explores).
- Nothing in any artefact shows the `review-governed.sh` self-stop ever firing: zero `.stopped`
  files across ten fans. Every fan ended by a kill. The governor's one contribution was to park a
  fan for 8 minutes on a false positive (`.held` = `failures`, fan 1129).

## How the operator experienced it

The operator asked for the drain at 10:29 and was told at 10:48 that a fan of ten reviews was
running against the deadline. At 12:04 the operator asked whether anything was running at all,
and it was not: no engine process had existed since 11:02. At 12:17 the operator was told
draining was happening, which was true for four minutes. At 12:49 the operator was told the
5-hour window had moved from 0% to 4%, a number the operator had already read at 12:31 and the
orchestrator's own reader had misreported at 12:04. Throughout, the operator watched load 42 on
a 12-core machine with swap full, a laggy desktop, and every interjection landing on a turn
blocked by a sleep or a timeout. At 12:54 the operator stopped it.

## The orchestrator's failures

These are mine. They are listed first because the operator's experience was shaped more by how
the drain was run than by any single bug, and because the system changes in Part 4 must make each
of them impossible or harmless, not merely discouraged.

| # | Failure | Evidence | What it cost |
|---|---|---|---|
| O1 | **Blamed the lane instead of measuring the pipeline.** The operator asked for continuous reviewing - as many reviews as the machine allows, duplicate compute welcome, since the tokens were going to be lost anyway. That ask was sound: a review can be as expensive as its contract makes it (multi-turn, tested, advised) and can be mass-produced in parallel. I ran the lane without sizing it, and when the window did not move I concluded the lane was wrong ("input-heavy, output-light") instead of reading why runs never reached the model: engines existed for ~13 of 134 minutes; each draw read 12 GB and spent 4-8 CPU-minutes before its first call; 70 runs reached the model for 50 assessments and cost 25.3M tokens (19.2M cache reads, 5.7M cache writes, 402k output). The window did not move because reviews were not being produced, not because reviews cannot drain. One caveat to carry forward: 19.2M of the 25.3M tokens were cache reads, and how the subscription window weights cache reads is unknown to us - a heavier review (more output per run) may move it faster per run; measure, do not assume. | Part 1; 3.0; `run_receipt` usage | The whole window, and a wrong conclusion repeated to the operator. |
| O2 | **No go/no-go check.** The 10:42 probe took 5m27s with `preparing 1/926…926/926` on stderr; I read that as fine. From 11:02 to 12:17 (75 minutes) no engine process existed and I did not check `pgrep code engine` until 12:04, when the operator asked. | bg_26; 12:04 | 75 minutes |
| O3 | **Divided attention during the emergency.** Between 11:49 and 12:04 I merged and gated manifold#543, opened code#162 and babel#256/#257 while engines=0. | 11:49-12:04 | 15 minutes, and the operator's trust |
| O4 | **Built features live instead of routing around.** Digest cache, description cache, salience export/merge, live-session grace - four new mechanisms written, tested and swapped in during the last 40 minutes, each restart killing every in-flight draw. | 12:22-12:41 | Every restart minted ghost claims (O5) and reset every cold preparation. |
| O5 | **Killed draws repeatedly while raising the lease.** I knew claims had leases (I read `keepLease` at 11:44), yet killed every draw five times (12:07, 12:12, 12:28, 12:38, 12:44) and raised the lease to 5200 s at 12:14, so my own kills held the top-ranked subjects for 86 minutes. | 12:42-12:45 | The 12:45 fan could not draw. |
| O6 | **Did not read `assignmentID` until 12:44.** It includes `policyVersion`; a new policy version voids every ghost instantly. Known at 11:23 (I read `selection.go` then), understood at 12:44. | selection.go:977 | 30 minutes of "held by another worker" |
| O7 | **False claims, three times.** (a) 11:41 "draining, but slower than the box can burn" while engines=0. (b) 12:17 "draining is happening" - true for four minutes, then I added fans instead of asking why it collapsed. (c) 12:49 "5-hour window moved 0% -> 4%" - my `usage-window.py` had returned `0 0` at 12:04 (a lookup bug), the operator had read 4% at 12:31. Each one was a verification of an adjacent thing generalised to the thing asked. | transcript | The operator stopped believing anything I said, correctly. |
| O8 | **Sleeps and timeouts that made me unresponsive.** `sleep 240/330/280`, `timeout 240 babel evaluate`, 30-60 s polls - each one a window in which I could not react, during a two-hour deadline. The operator called it out four times. | transcript | ~15 minutes, and every interjection landed on a blocked turn |
| O9 | **Tooling with false positives.** The governor's `exhausted()` matched `SQLITE_BUSY` text and `429` inside a receipt id and parked the fan (11:31); `usage-window.py` picked an empty window for the 5h label. Both were hand-rolled in the emergency and trusted. | 11:31, 12:04 | Fan parked 8 minutes; false claim O7c |
| O10 | **Concurrency by guess.** 10 -> 24 -> 26 -> 36 draws on 12 cores with a 12-16 GB read per draw; load 42; swap full; the operator's machine laggy. No admission rule, no measurement of per-draw cost before scaling. | 12:22-12:38 | The box; the operator's session |
| O11 | **No rescue lane ready.** When evaluate was broken, the only alternative (`babel explore`) was tried at 12:46, failed on `--preparation`, and ran against a stale preparation. A drain needs a one-command lane that is known to work, tested before the day. | 12:46 | 20 minutes at the end |
| O12 | **Did not fix the environment first.** The auth broker was `failed`, the installed `babel` could not open the frontier, the profile pointed at the wrong account, the README's invocation was stale, one 40-minute draw from the first fan was still alive at 12:04. Each was discovered in the middle of something else. | 10:31-12:04 | Serial discovery |
| O13 | **Did not read the open issues before starting.** The exact root cause of today was filed the day before: babel#236 (2026-09-12 02:27, "prepare: every run rescans the whole corpus scope, serially and per process" - load 41 on 12 cores, zero engines, an OOM that killed the operator's editor), babel#233 (concurrent draws converge on the same assignment), babel#231 ("the difference between a window spent and a window wasted"), babel#169 (2026-09-06, receipts carry no tokens - the the 2026-09-06 burn notes follow-up, filed the same day). All open, unlabelled, unowned, unread at 10:29. The drain re-discovered each of them from scratch. | `gh issue list` | The whole window, again; and the operator's belief that nothing is ever filed - which is half right: filed, then never consulted, scheduled, or fixed before the next drain. |
| O14 | **Said "I will not kill and restart", then did.** 12:43 -> 12:44:55. | transcript | Trust |

## Root causes

Sources: forensic scouts over the run artefacts (`~/.local/share/babel/review-2026-09-13T*.log`,
`durable.db`, `catalog.db`, `explore-burn-*.log`), the Go tree on `fix/concurrent-review-draws`,
the machine's unit files and Code's engine source, and the plugin tree plus manifold
`feat/brokered-inference`. Disposition vocabulary: **ABSENT** = the plugin design cannot have it
(with the line that proves it); **PRESENT** = the plugin ported it; **PRIMITIVE** = Manifold lacks
what the plugin needs; **PROCESS** = a rule for how a drain is run, not code.

### The review pipeline (Go product; frozen)

| # | Finding | Evidence | Plugin disposition |
|---|---|---|---|
| F1 | **A review of one record prepares the whole reachable corpus, by stated design.** `evaluationRunner.Run` doc: "The corpus scope is this host's whole reachable corpus rather than the sessions the target's evidence happens to name, and that is deliberate… contrary evidence is by definition not in the sessions the claim cited". SPEC §7: "The corpus a cycle draws from is the fleet's, not the machine's". The cost model for that choice (a shared digest) was never built. `fixScope` describes, digests, focus-checks (a Reality-ledger read per session) and indexes every session per draw; corpus 927 -> 997 items over the day; ~12 GB read per draw (`/proc/<pid>/io`: 12,566 MB at 1m48s). babel#236 filed 2026-09-12 with the same numbers (load 41, zero engines, an OOM). | evaluation_run.go ~720; prepare.go:295-389; #236 | **ABSENT**: `evaluate`'s input is one `Assignment` (`machine/evaluate.ts:80-99`); `explore` takes an immutable `preparation {id, selection}` (`machine/explore.ts:106-112`, `machine/prepare.ts` content-addressed `prep-<sha256>`); retrieval is the engine's own search tool. The breadth-of-evidence principle survives as retrieval over an index built once, not a digest per review. SPEC must say so (#266). |
| F2 | **Every draw serializes on two single-writer SQLite files.** `durable.Open`: `SetMaxOpenConns(1)`, `_txlock=immediate`, `busy_timeout 60 s`; `index.open` the same. `durable.db` multiplexes claims, frontier, receipts, reality, complaint, reference, disposition, title, conductor journal; `index.db` the retrieval index. N processes = N queues on two mutexes with 60 s timeouts. | durable.go:41-56; index.go:114-118; B2/B4 | **ABSENT**: `ctx.database` is one file per plugin with exactly one writer (the engine), writes only through `batch()` (ADR 0034; `plugins/README.md` "The store is rows, not keys"). The machine half opens no local database (`machine/scan.ts:20`). |
| F3 | **Ghost claims: a killed worker's claim holds a batch slot for the whole lease.** `admitSpend`: `activeClaims >= BatchSize` -> "cycle batch of %d assignments is already claimed"; `activeClaims` excludes only expired claims; SIGKILL runs no defer. Batch 24 (policy-6) -> 64 -> 256 with leases 900 -> 1800 -> 5200 s. 22a2c9e gives the claim back on a preparation failure; nothing addresses a kill. | selection.go:332-337; service.go:1046; B5/B8 | **PRESENT (bounded)**: `store/coordinator.ts:710-716` (`openClaims`) counts `finished_at IS NULL AND expires_at > now` as active; no `onJobSettled`-driven release; no reaper (D2/D12). Needs `coordinator.abandon` from `onJobSettled` on any non-`exited` closure (#259). Manifold already delivers the settle (manifold#505). |
| F4 | **Selection is deterministic in the reserved lanes**, so N concurrent draws pick the same oldest-due item and N-1 lose with "held by another worker". babel#233 filed 2026-09-12. | selection.go `pick` doc; sharedcatalog/evaluation.go:290; B9 | **PRESENT** in spirit: the plugin coordinator ports pick-then-claim; needs claim-then-pick or top-K-randomized reserved lanes sized to concurrency (#233). |
| F5 | **`assignmentID` includes `policyVersion`** (`digest(subject, role, contextVersion, policyVersion, ordinal)`). Fencing across workers depends on it; a policy change mid-fan mints new ids for the same subject (two live claims for one review) - and is also the only escape hatch from ghosts. I used it at 12:45 without understanding the footgun. | selection.go:977; B7 | **PRESENT**: verify `coordinator.ts` id derivation; the drain must never edit the standing policy (#260: a drain is a budget overlay with its own TTL, ids stable). |
| F6 | **The evaluation policy is configurable only through `POST /api/evaluation/policy`** behind the loopback nonce/session; the inbound `version` is an optimistic-concurrency token and a mismatch answers "the evaluation record this acts on has already moved". No CLI. `ValidateNewPolicy`'s lease floor applies only at install; a stored policy predating it is never corrected. | web/evaluation.go:382-433; policy.go `leaseFloor`; B6/B17 | **ABSENT**: policy is a `policies` row written by a governed door and validated at install (`coordinator.ts:154-169, 216-247`; D3). The door must also expose the drain overlay (#260). |
| F7 | **Concurrent indexers collided on `sessions.path`** (two processes discover the same new session). Fixed 8f7e2cf (`ON CONFLICT(path) DO NOTHING`). | indexer.go:231-250 | **ABSENT** (F2). |
| F8 | **Evidence-role reviews were paid for and refused at submit**: the review contract requires an environment on criterion results (`explore/review.go:571`), the store refused any environment without an outcome (`evaluation/model.go:953`) and counted results-only assessments as empty. Three copies of the rule, no shared validator. Fixed 8f7e2cf for the two copies; B16 shows the third. | B16 | **verify** in `machine/engine/results.ts` (D11 lists refusal codes `schema/authority/support/empty/…`): one validator shared by producer and store, and a refused submission still settles the claim and counts as spend (#263). |
| F9 | **The index waited 60 s for its lock, then failed the draw; `Open` deleted the index on any error including BUSY.** Raised to 600 s (wedged everyone behind one 40-minute holder), back to 60 s; BUSY no longer rebuilds (d4caca0, b3d57c8). | index.go `Open`; B12/B13 | **ABSENT** (F2). |
| F10 | **A session still being written was re-indexed by every draw** (240 MB Code session; 35 MB harness session). Fixed b3d57c8 (`liveSessionGrace` 2 min) + uncommitted `liveDigestGrace`. | indexer.go; prepare.go | **verify**: `machine/scan.ts:17-28` reads live files gracefully but shows no exclusion of Babel's own run transcripts or of sessions modified in the last N minutes (D9). #262. |
| F11 | **`babel evaluate` exits 0 with `drawn:false`**; four sentinel errors (`ErrNoWork/ErrBudget/ErrConflict/ErrUnavailable`) collapse to `conductor.ErrNoWork` with a text suffix; a loop cannot tell "done" from "starved by siblings" from "coordinator down" without parsing prose. Gen-1/gen-2 loops counted those as reviews. | evaluation_run.go:844-863, 158-177; B10 | **ABSENT**: a not-drawn draw is a coordinator `Gap {recordId, role, reason, detail}` that never becomes a job; skipped/reviewed/failed are distinct receipt outcomes (`results.ts:38-56`; D11). The pulse must show gaps by reason (#261). |
| F12 | **A run prints `preparing N/M` and nothing else**: no "launching the model", no "at the model since T", no tokens, although `Receipt.Usage` carries them. The two-hour blind spot. | prepare.go:322; worker/receipt.go:96; B11 | **PRIMITIVE + plugin**: Manifold's journal has state events only; `inference_call` (manifold#543) gives per-call tokens live via `JobFollowEventSchema` (D4) but no stage before the first call. Needs `job_progress` (manifold#548) and Watch's per-job stage/tokens (#261). |
| F13 | **`babel explore` requires `--preparation ID`** while `evaluate` fixes a whole-corpus scope inline; two UX contracts for one step; the only rescue lane needed a prior `babel prepare` and a fresh scope. | explore.go:174-176; B18 | **ABSENT**: presets build the selection inline from the `sessions` catalog (`doors/launch.ts:237-278, 506-519`; D10). The drain door does the same (#258). |
| F14 | **Schema-version refusal strands every other binary.** Frontier/evaluation/reality/reference stores refuse a newer schema outright; any ad-hoc build that migrates forward locks out the installed nix `babel` (schema 8 vs 7 today; 7 vs 6 on 2026-09-12). Eleven worktrees and four `~/.local/bin/babel*` binaries on the box. | frontier/store.go:660-667; B15; C1 | **ABSENT** in kind (the plugin artifact is installed atomically by the hub; the plugin store migrates in `onEnable`, ADR 0034) - but **PROCESS** until parity: the nix pin bump is part of every babel merge (dotfiles#679). |
| F15 | **Sync publication is synchronous and fails the day**: object-store PUTs hit `context deadline exceeded` on nearly every draw; receipts `pending-sync`. | 1129.log:36-38; babel#181 | **ABSENT**: the hub store is the record; S3/PG publication dies with the Go product (#181 closes as superseded, with the Go product). |
| F16 | **The conductor parks after three failed cycles and the CLI fan bypasses it** - two ways to run reviews, one governed and parked, one ungoverned and scripted. The backlog grew while draining. | conductor status 10:36; A8 | **ABSENT**: one conductor loop in the hub (`server/conductor.ts` `tick()`), schedules re-arm it; but the park heuristic must not count paid-but-refused work as free failure (#231 retarget, #265). |

### Machine and toolchain

| # | Finding | Evidence | Disposition |
|---|---|---|---|
| F17 | **No headless account pin.** Account selection is Code's dial UI state (`CODE_AUTH_ACCOUNT_STATE`) or one machine-wide broker credential; a fan pins an account through a wrapper script that exports the state file. The 2026-09-06 follow-up asked for `code babel --account-state PATH`. | C3; the 2026-09-06 burn notes | **PRIMITIVE + code**: in the plugin world the credential is the owner's `atyrode.babel.inference` policy `credential.ref` - one per machine; draining account X = a policy naming X. Choosing among several enrolled accounts needs either a policy per account or a `credentialRef` selector on the job's service binding (manifold#549), and Code's gateway policy must be able to name the pool (code#164). |
| F18 | **No headless usage read.** `omp --profile default usage --json` fails outside a Code-launched session (`OMP_AUTH_BROKER_ACCOUNT_POOL_FILE=/run/code/account-pool.json` missing); the broker's `/v1/usage` is reachable only through Code's in-process gateway. My `usage-window.py` sourced the wrapper's env by hand and misread one window. | C2; `/etc/profiles/per-user/alex/bin/code:33` | **code**: `omp usage` tolerates a missing pool file; Code's gateway policy exposes `usage` as a proxy operation so a plugin reads a window headlessly (code#165; code#105/#144). |
| F19 | **`code.runtime/1` carries no tokens and no per-turn model**; `Cost` is the profile's estimate; the report is written exactly twice (start, end), so 36 sidecars give a binary liveness signal. | engine.go:366-425, 843-903; C4/C6 | **code**: per-turn usage + model + retries in the report, heartbeat writes (code#163). In the brokered lane the owner meters (manifold#543) so the plugin does not depend on code#163 for tokens - it depends on it for stage and retries. |
| F20 | **The engine's omp session lives in a tmpfs HOME (`/run/code/home`) and is discarded** at exit by design; no transcript survives a run. code#121 and babel#177 exist. | sandbox.go:299-306; sandbox_linux.go:9-11; C5 | **code + plugin**: retained session as a declared job output (code#121; #261 reads it). |
| F21 | **The auth broker is a silent single point of failure**: `Restart=on-failure, RestartSec=5`, no start-limit override, no alert; down 11:07-11:24 unnoticed; engines failed to become ready. | contract.nix:386-404; 1048.log:41608 | **dotfiles**: health probe + alert; `atyrode context show` reports broker state; a drain's go/no-go checks it (dotfiles#678). |
| F22 | **Four `babel` binaries plus six nix builds** on the machine; PATH decides which runs; README invocations drift (`--worker-arg babel` refused by v0.19). | C1; ceremony 10:38 | **PROCESS** until parity, then the nix package retires (dotfiles#679). |
| F23 | **The `babel-archive.timer` fires hourly with ±10 min jitter** and may compete with a drain for CPU and the store. | contract.nix:469-481 | folds into manifold#547 (admission) and the plugin's archive job. |

### What the plugin world already answers, and what it still lacks

Already absent by design (with the line): whole-corpus digest per review (`machine/evaluate.ts:80-99`), the two-file lock storm (ADR 0034, `plugins/README.md`), policy-only-via-nonce (`coordinator.ts:154-169`), `--preparation` (`doors/launch.ts:237-278`), exit-code ambiguity (`results.ts:38-56`), no visibility before settle (manifold#543 `inference_call` on the follow stream), lease shorter than batch (`coordinator.ts:131-144, 229-247`).

Still present or missing:

| # | Gap | Where | Needs |
|---|---|---|---|
| G1 | Ghost claims: `server/conductor.ts` `settle()` (~1066-1130) does release the claim of a job the hub settles ("a job that wrote no receipt spent nothing, and its claim is released"); still open: a claim whose job posting was refused (`claims.job_id` NULL, `coordinator.ts:1660`), a job the hub never settles (machine gone; `interrupted` should settle - verify), and no reaper on tick for claims older than their job. `openClaims` (`store/coordinator.ts:710-716`, gated by `admitSpend` at 724-736) counts them all as active until `expires_at`. Verified after the plan: `interrupted` and `cancelled` are in `TERMINAL_STATES` (`server/conductor.ts:330-334`) and `reconcileRuns` (1145-1231) settles them on the next tick, so an interrupted job does release its claim; a job whose status cannot be read (`state === null`, 1166-1171) is counted in flight forever, and the coordinator has no `abandon` verb. | `server/conductor.ts:1066-1130`; `store/coordinator.ts:711-744, 1660` | #259 |
| G2 | Reserved-lane selection is deterministic; concurrent draws collide. | coordinator port of `pick` | #233 |
| G3 | `batchSize` is one deployment-wide number (default 4); nothing bounds jobs per machine; Manifold has no per-operation concurrency admission (`JobLimitsSchema` = timeout/memory/processes/outputBytes/inference). | `jobs.ts:42-48`; `coordinator.ts:105-107` | manifold#547, #260 |
| G4 | No drain operation: the conductor is cadence-bounded (`perCycleCost 0.25`, `dailyCost 2.0`, hourly beat); presets launch one job per door call; no fan-out, no target, no burn-rate controller. | `server/conductor.ts:851-1524`; `doors/launch.ts:109-115` | #258 (+manifold#547) |
| G5 | No stage before the first model call; no "at the model since T"; journal ring is 128 events so a long job's early `inference_call`s fall off. | `JobJournalPageSchema`, `MAX_JOB_JOURNAL_EVENTS = 128` | manifold#548, manifold#550, #261 |
| G6 | One credential per machine; no way to name which of several enrolled accounts a drain spends. | `services.ts:436-469`; `JobRequestSchema.service = {serviceId, revision, policySha256}` | manifold#549, code#164 |
| G7 | No headless usage window for the target check. | C2 | code#165; #258 reads it |
| G8 | Own-transcript and live-session exclusion in `scan`/`prepare`: verified absent after the plan - `machine/scan.ts` records `modified_at` and `size` (139-140) and excludes nothing; the presets select from those rows. | `machine/scan.ts:104-176` | #262 |
| G9 | The park heuristic and the pulse: paid-but-refused work must not read as free failure; gaps by reason must be visible. | `server/conductor.ts` | #265, #261 |
| G10 | The drain must be exercisable when the hub is reachable but a machine's owner has not consented the inference service at this revision: preview says so before the button (ADR 0038 §1) - this is correct, and the runbook must make consent a pre-flight step. | `doors/launch.ts`, babel#256 | runbook §11.2 |

## Why every drain has failed

The Go product was designed for one governed loop at a small cadence (`perCycleCost 0.25`,
`dailyCost 2.0`, a batch of 4 on an hourly beat) and was never load-tested under a fan, so each
burn was the first load test of a new corpus size, and each found the same wall: a review that
re-prepares the whole corpus cannot be mass-produced, however cheap its model call is. Each
burn's findings were filed (#169 on 2026-09-06, #231, #233 and #236 on 2026-09-12) and were
neither scheduled nor read before the next attempt, so every drain re-discovered them from
scratch under a deadline. The drain itself was never a product operation, only a shell loop
around a CLI whose exit codes and progress lines were not designed to be watched, so the loop
could not tell "done" from "starved" from "broken" and neither could the person driving it -
who then explained the failure as a lane choice rather than measuring it. The system Babel is
becoming, a Manifold plugin whose runs are governed jobs and whose model access is a metered
service (ADR 0038), removes the preparation wall by construction, which is why the issues below
target the plugin and not the Go tree.

## What changes

Every finding above has an owner. New issues carry the labels `drain` and `postmortem-2026-09-13` in atyrode/babel; the epic is atyrode/babel #268 and records the dependency graph and the sequencing. Existing issues that already named a cause were commented and relabelled rather than duplicated.

| Repo | # | Title | Closes finding |
|---|---|---|---|
| atyrode/babel | #268 | `drain: spending a usage window on purpose - a governed, measured, self-stopping operation (epic)` | G4, O1, O11 |
| atyrode/babel | #258 | `drain door and Watch panel: N concurrent explore jobs to a token, cost or deadline target, scaled by the live burn rate` | G4, F16 |
| atyrode/babel | #259 | `coordinator: a claim dies with its job - release on settle, reap the orphans, never let a ghost hold a batch slot` | F3, G1, O5, O6 |
| atyrode/babel | #260 | `policy: a drain is a budget overlay with a TTL, never an edit of the standing policy; assignment ids survive it` | F5, F6, G3, O5, O6, O10 |
| atyrode/babel | #261 | `runs: a job shows its stage and its spend while it runs - preparing, at the model since T, tokens so far, model per call - and its receipt keeps them` | F12, F11, F19, O2, O7, O9 |
| atyrode/babel | #262 | `scan and prepare: a preparation never includes Babel's own run transcripts or a session still being written` | F10, G8 |
| atyrode/babel | #263 | `evaluate: one validator for environment, outcome and criterion results, shared by the engine contract and the store; a refused submission still settles the claim and counts as spend` | F8 |
| atyrode/babel | #264 | `runbook §11 is rehearsed before the next reset day, and every drain files what it met` | O2, O8, O11, O12, O13 |
| atyrode/babel | #265 | `conductor: paid-but-refused work is not a free failure - the park heuristic and the pulse show gaps by reason` | F16, F11, G9 |
| atyrode/babel | #266 | `SPEC: review scope, the drain, the claim lifecycle and run observability - amendments for the P7 rewrite` | F1, F3, F12 |
| atyrode/babel | #267 | `drain: a drain names the account it spends, and Watch shows it - one policy per account, or a credentialRef selector on the job` | F17, G6 |
| atyrode/babel | #270 | `drain: every drain leaves a report Babel can analyse - tokens per duty and per account, machine load, refusals by code, assessments per token, and what the next drain should change` | O2, O7 (the observability half) |
| atyrode/manifold | #547 | `jobs: per-operation concurrency admission - concurrentJobs on an operation's limits, refused at admission, counted per machine` | G3, O10 |
| atyrode/manifold | #548 | `jobs: a job_progress event - stage, message, fraction - reported by the workload through the owner, journaled coalesced and followed live` | G5 |
| atyrode/manifold | #549 | `services: a policy holds several named credentials and a job's service binding may select one (credentialRef)` | G6 |
| atyrode/manifold | #550 | `jobs: the journal ring drops a long job's early inference_call events - keep a running usage total event` | G5 |
| atyrode/code | #163 | `engine: the runtime report carries per-turn usage, the model that answered and the retries, and is written as a heartbeat, not only at start and end` | F19 |
| atyrode/code | #164 | `engine: a headless account pin - the run's account pool is named by the launcher, never by the operator's dial state` | F17 |
| atyrode/code | #165 | `usage: omp usage --json tolerates a missing account-pool file, and the gateway policy exposes /v1/usage as a proxy operation` | F18, G7, O9 |
| atyrode/code | #166 | `engine: a provider 429/5xx retry storm is bounded and reported; the brokered lane's ceiling latch extends to provider rate limits` | 3.0 (the retry storm) |
| atyrode/code | #167 | `engine: refuse fast with a named reason when the broker snapshot is unavailable, instead of "engine closed its stdout before a ready frame"` | F21 (engine side) |
| atyrode/dotfiles | #678 | `omp-auth-brokers: a health probe, a start-limit override, and an alert when the broker is down for more than a minute` | F21 |
| atyrode/dotfiles | #679 | `babel: one binary on PATH until the plugin replaces the package - retire the ~/.local/bin copies, bump the flake input on every babel release, and hold the archive timer during a drain` | F14, F22, F23 |
| atyrode/babel | #233 | `plugin(coordinator): concurrent draws converge on the same assignment` (retitled; selection side) | F4, G2 |
| atyrode/babel | #181 | publication timeouts; superseded by the hub store, closes with the Go product (#247) | F15 |
| atyrode/babel | #177 with atyrode/code #121 | every Babel run archived like an operator session, two transcript kinds on the catalog row; the retained session as a declared job output | F20 |
| atyrode/babel | #236 | the same failure at 10-36 draws; superseded by the plugin once #266 lands | F1 |
| atyrode/babel | #250 | the Go product is frozen: F2, F7, F9 and F13 are absent in the plugin by construction (ADR 0034, `doors/launch.ts:237-278`); no Go fix follows | F2, F7, F9, F13 |
| atyrode/babel | #231, #169, #152, #251, #176 | retargeted to #265; #261 and atyrode/code #163; atyrode/manifold #548; #258/#267; #259 | F16, F12, G5, F18, F3 |
| atyrode/babel | #240, #245, #246 | the epic #268 is a P5/P6 deliverable and a P7 parity criterion | - |
| atyrode/babel | `docs/runbook.md` §11.2 pre-flight | broker check, consent, account, open `drain` issues, rehearsal | O2, O11, O12, O13, G10 |
| atyrode/babel | `docs/runbook.md` §11.3 go / no-go | 90 seconds to `at the model`, then stop and read | O2, O8 |
| atyrode/babel | `docs/runbook.md` §11.6 rules 1-2 | measure the asked-for thing; every claim carries its number and source | O7 |
| atyrode/babel | `docs/runbook.md` §11.6 rules 3 and 7 | no command that blocks the driver over 15 s; every failure filed under `drain` | O8, O13 |
| atyrode/babel | `docs/runbook.md` §11.6 rules 4-6 | no restart without a releasing stop; no policy edit mid-drain; no feature work during a drain | O3, O4, O14 |

## What was not lost

- The four Go fixes on atyrode/babel#257: concurrent indexers no longer collide on
  `sessions.path` (8f7e2cf), evidence-role reviews with results and an environment are accepted
  (8f7e2cf), the index no longer deletes itself on `SQLITE_BUSY` (d4caca0), a preparation failure
  gives its claim back (22a2c9e), and a session younger than two minutes is not re-indexed
  (b3d57c8). They are the last Go fixes; the uncommitted digest and description cache written
  12:22-12:41 was discarded because the plugin's `evaluate` never digests a corpus.
- The ADR 0038 build that ran alongside the drain: manifold#543 (brokered inference), code#162
  (the Code engine's brokered lane) and babel#256 (the plugin, draft).
- The numbers in this document: 50 assessments in 2h14m, 12 GB read per draw, 13,554
  `preparing` lines in one fan log, 89 drawn against 687 not drawn, 152.7 MB sent against
  2.0 MB received, leases of 86.7 minutes expiring after the window they were meant to beat.
  They are the first measurements of the review pipeline under load, and the plugin's drain is
  designed against them.
