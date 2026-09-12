# Babel as a Manifold plugin — the plan

Decision 91, 2026-09-12 (SPEC.md §2.8). Babel is rewritten as a Manifold plugin
family, in TypeScript, in this repository, with the hub owning its state, the
existing records imported once, and anything Manifold lacks built in Manifold.
This document is the plan of record; the specification is rewritten section by
section as each phase lands, and until then a section that describes the Go tree
describes the past.

Every fact about Manifold below is read at `atyrode/manifold` main `eccd7b9c`
and every fact about the code plugin at `atyrode/code` main `24bf50c`, both as
sibling checkouts on dev-01. Every test of the plugin runs on
`preview.manifold.tyrode.dev` or a local preview-equivalent hub;
`manifold.tyrode.dev` is installed by the operator, by hand, from a release.

## 1. What carries over

The product, not the implementation:

- **One list the operator rules on.** Home is every record Babel produced,
  ranked by what needs him, hot, new, top, controversial, rising; the queue is
  a filter; his acts are rulings and asks; the score is Babel's reviewers' only
  (§8.7).
- **Babel votes; the operator rules.** Reviewers vote per role on exact
  revisions and are legible as voices; where they split is the front page's
  real signal.
- **Topics are ledger entities.** Filing is an append-only link; interest is
  the operator's fact; every change to a topic is an ordinary proposal through
  the chain (§4.13).
- **Babel handles Babel.** Filing, consolidating, superseding, retiring and
  promoting to memory are Babel's own outputs, reviewed by its reviewers, ruled
  by the operator; the least magic wins.
- **Observations are evidence, not posts;** nothing is stale by a clock;
  nothing is deleted.
- **The operator is the boundary.** Only his acceptance creates an entity,
  asserts a fact or applies a plan.

What is retired: the Go tree, `babel web`, the standalone loopback security
model (§2.7), the shared PostgreSQL catalog, the sealed-payload S3 object store,
the plaintext allowlist, the publication journal and `babel sync`, the fleet
reader, the per-machine "durable" store as a thing to be synced. The hub is the
one place; machines are where sessions live and where runs execute.

## 2. Architecture: three halves

```
                         atyrode.babel (hub)
   ┌──────────────────────────────────────────────────────────┐
   │ server half (in-realm, Bun)           web half (React)   │
   │  doors: rulings, asks, interest,       Home · Record ·   │
   │         launch, answer, tell           Topics · Watch ·  │
   │  store: plugin database (SQL)          Settings          │
   │  jobs:  declares operations,           @manifold/ui +    │
   │         follows runs, ingests outputs  plugin CSS        │
   └───────────────▲───────────────────────────▲──────────────┘
                   │ engine.jobs                │ host services
   ┌───────────────┴───────────────────────────┴──────────────┐
   │ machine half (Bun-compiled binary, delivered as a job    │
   │ artifact per platform)                                   │
   │  scan      sessions on this machine → catalog rows        │
   │  archive   restic backup of session roots                 │
   │  prepare   fixes a corpus scope from selectors            │
   │  explore / evaluate / conductor  drive `code engine`      │
   │  (omp RPC on stdio) under a profile, emit records +       │
   │  receipts as job outputs                                  │
   └───────────────────────────────────────────────────────────┘
```

- **Server half** (`plugins/atyrode.babel/server.ts`): the doors (`ActionCtx`)
  the web half and the machine half call; owns the store; declares the machine
  operations; registers the schedules; ingests job outputs into the store;
  builds the feed index. Everything the Go `internal/web`, `internal/frontier`,
  `internal/evaluation`, `internal/reality`, `internal/disposition` did, minus
  publication.
- **Web half** (`web.tsx` + sub-plugins): panels Home, Record, Topics, Watch,
  Settings on `@manifold/ui` layout primitives (`Stack`, `Cluster`,
  `Switcher`, `ScrollRegion`), a scoped CSS module carrying the design tokens
  the standalone UI settled on (graphite palette, one chip, two buttons, the
  4-px scale, editorial serif for headlines), keyboard vocabulary unchanged
  (`j k ↵ y n d f q s c m`), polled resources for liveness, the Job follow ring
  for runs in flight.
- **Machine half** (`plugins/atyrode.babel/machine/`): one Bun binary with the
  five operations above, declared in the plugin's `MachineHalfSchema`
  (`protocol/src/jobs.ts:241`), delivered and pinned per platform through
  `JobArtifactDelivery`/`MachineArtifact` (`jobs.ts:38`). The session adapters
  (omp, codex, claude), the retrieval index, the restic invocation and the
  engine runner (the omp RPC client, result schemas, receipts) are rewritten
  here in TypeScript; `code engine` stays the engine.

Dependencies: `atyrode.code` (required) for the engine and, when its `usage`
and `accounts` sub-plugins ship, for the model, profile, cost and usage
windows Watch shows before a run starts.

## 3. Storage: the plugin database

`plugin_kv` is a namespaced string KV with 64 KiB values and a migration
ledger (`packages/plugin/src/storage.ts`). Babel's data is a graph — 65,818
records, 60,793 links, 3,037 observations under 2,445 hypotheses, filings,
tallies, dispositions, a ledger of entities and facts — read by filter, sort,
join and aggregate on every page. A KV that cannot query is not a store for it.

**The primitive to build (Manifold ADR):** a **plugin database** — a per-plugin
SQLite file under the hub's data directory, opened by the hub, exposed as
`ctx.database` with prepared statements, transactions and the same versioned
migration ledger `plugin_kv` has; purge deletes the file; two plugins never see
each other's; in-realm calls are synchronous inside and promise-returning
outside (ADR 0016's one-contract rule); isolated plugins cross the RPC
boundary with the same contract. Limits stated in the ADR: statement time,
row and byte caps per call, no attached databases, no extensions.

Babel's schema in it (plaintext, on the operator's own hub, one place):
`sessions` (the catalog: host, harness, source id, digests, workspace,
repository identity), `records` (hypothesis/observation/finding/proposal with
revisions), `links`, `filings`, `status_events`, `dispositions`,
`assessments`/`feedback`/`claims`/`policy` (evaluation), `entities`/`aliases`/
`facts`/`questions`/`plans`/`focus_rules` (reality), `receipts`, `complaints`.
Identifiers are kept as they are today so the importer preserves provenance.

Sessions themselves stay where they are: archived by restic from each machine
into the repository the operator configures, with the repository password
reached through an **Instance Service** policy (`protocol/src/services.ts`)
rather than a file the plugin reads. The hub's catalog rows point at
snapshots; fetching a session another machine archived is a job on the
machine that holds the repository access, exactly as `babel fetch` does today.

## 4. Machines, jobs and schedules

Everything Babel executes on a machine is a **Job** (`protocol/src/jobs.ts`):
a declared operation with typed inputs, input/output file bindings, required
runtime tools (`git`, `restic`, `code`), service bindings and confinement,
spawned and journaled by the agent, with sealed outputs and a bounded follow
stream (128 events / 256 KiB) the web half reads for liveness.

| today (Go, PTY or cron) | plugin |
|---|---|
| `babel scan` / catalog refresh | `scan` operation, scheduled per machine |
| hourly `archive push` (dotfiles timer) | `archive` operation, scheduled through `engine.jobs.schedule` (`job-doors.ts:93`, `JobScheduleTiming`) |
| `babel prepare` | `prepare` operation (selectors in, preparation id + selection out) |
| `babel explore` | `explore` operation: preparation + recipes + caps in; records, receipts, questions out as output files |
| `babel evaluate` | `evaluate` operation: assignment in (claimed by the server half's coordinator, leases renewed by the job's heartbeat), assessment out |
| `babel conductor run` | a **server-half loop** over `engine.jobs`: draws, launches, ingests, on a schedule — the conductor's policy is store state, not a process |
| Watch's launch form | doors that create job requests from presets and topics |

Job outputs are the transport for records: a run writes its records and
receipt as output files; the server half ingests them into the database on
completion. Outputs larger than an action body (1 MiB) need the large-payload
path in §7.

## 5. Engine and transparency

Runs drive `code engine --profile … --runtime-info …` on the machine over omp's
RPC, as `internal/explore` does now; the machine half owns that client. The
runtime report (`code.runtime/1`) gives the profile, disclosure, cost per 1k
and the worker version before the first byte, so Watch states *what will run*
from the same source; usage windows and balances come from the code plugin's
`usage` sub-plugin (`plugins/atyrode.code/usage/web.tsx`'s `UsageView`, fed by
its service policy over `/v1/usage`) once it ships, and Babel does not
reimplement them.

## 6. UI

In-realm, so the crafted surface carries: the feed with its vote strip, the
sentence toolbar, hover acts, the peeled record, topics with interest,
Babel-proposes, the live pulse; motion through CSS and the host's own panel
transitions. `@manifold/ui` for layout; Babel's tokens in its CSS module; no
DOM tricks that assume a page of its own. Panels: `home`, `record`, `topics`,
`watch`, `settings`; elements: a record, a topic, a run; a `babel` discipline
and sections per the contributes vocabulary (`docs/PLUGINS.md` §2).

## 7. What Manifold lacks — to build

Each becomes an issue and a PR in `atyrode/manifold`, tested on a preview:

1. **Plugin database** (§3) — the one Babel cannot start without.
2. **Large-payload path** — a run's output set exceeds 1 MiB; either output
   files readable by the owning plugin's server half through the job API, or
   a plugin-owned upload route. Prefer the former: outputs already exist and
   are sealed.
3. **Job journal read API** — the run page reads a receipt as a story; the
   follow ring is live-only and non-replay; a bounded read of a finished job's
   journal is needed.
4. **Machine-scoped grants and domain capabilities** — "back up sessions on
   this machine", "observe this repository": the addressable node kinds have
   no machine node and `CAPS` is a closed nine-member enum.
5. **Declared machine tools** — a way for the roster to say a machine has
   `restic`/`git`/`code` before a job is scheduled there (filed as
   atyrode/manifold#153/#190 by both projects).

Schedules exist (`engine.jobs.schedule`) and are not a gap.

## 8. Phases

Each phase ends with something visible on `preview.manifold.tyrode.dev`.

| phase | lands | proof on preview |
|---|---|---|
| **P0** | this plan; SPEC §2.8 reversal; ADR for the plugin database in `atyrode/manifold`; `plugins/` scaffold on the kit at manifold main; CI packs and delivers to preview | the plugin manager lists `atyrode.babel` with an empty Home |
| **P1** | Manifold: plugin database; job outputs readable by the owner; journal read | a kit `verify` run exercising all three |
| **P2** | store schema + **importer** (durable.db + shared catalog → database, ids kept); Home, Record, Topics read-only over the imported data | the operator's 65k records, votes and topics readable on preview |
| **P3** | rulings, asks, answers, interest, Tell Babel as doors; the feed index; live pulse | the operator rules on preview and it sticks |
| **P4** | machine half: `scan`, `archive`, `prepare`; schedules; catalog rows from enrolled machines | dev-01's sessions catalogued and archived from a job |
| **P5** | `explore`, `evaluate` operations; the evaluation coordinator (claims, leases, policy) in the store; receipts; Watch with presets, recipes, *run it on a topic*, model and ceiling up front | a run started from preview writes records and votes into the store |
| **P6** | conductor loop; filing and backlog lanes; topic plans applied by rulings | Babel proposes a topic on preview and the operator accepts it |
| **P7** | parity review against the product list in §1; cutover: production install by the operator; Go tree, `web/`, PostgreSQL and the S3 store retired; SPEC rewritten | `babel web` gone from dotfiles; the plugin on `manifold.tyrode.dev` |

## 9. Working method

- Layout and tooling copy `atyrode/code`: `plugins/atyrode.babel/{manifest.json,contract.ts,server.ts,web.tsx}`
  with sub-plugins as directories inside it; `contract.ts` is the one source of
  truth for ids, doors, storage keys and events; `bun run check`, `bun test`,
  `bun run pack`, `bun run verify` before every push; the dev loop from dev-01
  is `bun run dev -- --hub http://127.0.0.1:7912 --deliver docker:manifold-dev-manifold-1`.
- The owner key never appears in argv, logs or files; delivery goes through
  the docker receiver only.
- Every PR names the hub, the panel, the action and the expected observation.
- The Go product keeps running and keeps being fixed for the operator's daily
  use until P7; no feature work lands in it after this plan.

## 10. Open questions for the operator

Asked through the ask tool as they arise; the ones known now are in the
conversation that ratified this plan.
