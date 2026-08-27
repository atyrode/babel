# Babel specification

Status: exploration draft

## 1. Purpose

Babel analyzes archived conversations between an operator and coding agents and turns them into evidence-backed opportunities to improve:

- the operator's systems and repositories;
- the way the operator communicates and collaborates with agents;
- agent instructions, rules, skills, and reusable processes;
- missing tools and automation;
- recurring product, code, security, and operational problems; and
- effective patterns worth preserving and repeating.

The project exists to close a feedback loop:

> conversations produce evidence; evidence produces findings; repeated findings produce proposals; human-reviewed proposals improve future conversations and systems.

Babel is not an autonomous remediation system. Its boundary ends at analysis, synthesis, and reviewable suggestions.

## 2. Product boundary

### 2.1 Babel owns

- the agent-session archive format, source catalog, host namespacing, and retention semantics;
- client-side encryption configuration and append-only upload/read-only retrieval behavior;
- archive status, integrity verification, and recovery-compatible restore commands;
- read-only ingestion of materialized chat archives;
- format adapters for supported agent session formats;
- normalization, indexing, deduplication, and provenance;
- deterministic preflight checks such as likely-secret detection;
- a versioned cookbook of AI-assisted analysis recipes;
- incremental analysis and cross-session synthesis;
- evidence-backed findings and proposal generation;
- a primary, keyboard-driven terminal interface from which the complete interactive product is reachable; and
- local review state and exports for humans or downstream tools.

### 2.2 Babel does not own

- collection of live conversations;
- machine-specific credential authorities, package installation, or service scheduling;
- editing repositories or agent configuration;
- opening or updating GitHub issues;
- credential rotation or incident response;
- silently sending transcript content to a hosted model; or
- deciding that a suggestion is correct without human review.

These exclusions are safety boundaries, not a promise that no future companion automation will act on accepted proposals.

### 2.3 Integration boundary with `atyrode/dotfiles`

`atyrode/dotfiles` already archives OMP, Codex, and Claude Code sessions hourly to a Clever Cloud Cellar S3 bucket. It uses an `rclone crypt` remote named `archive`, stores machine-local configuration in `~/.config/atyrode/session-backup/env`, writes data below `archive:<hostname>/`, and uploads with `copy`/`copyto` rather than `sync`. Babel will adopt this proven archive contract without changing the existing remote layout or crypt material.

Ownership follows dependency direction:

- **Babel owns portable archive behavior:** supported sources, archive layout, host namespacing, client-side encryption configuration, append-only upload, read-only download, status, integrity checks, retention rules, and restore commands;
- **dotfiles owns machine convergence:** installing and pinning Babel, enabling it per host, supplying credentials from the operator's secret authority, and declaring systemd or launchd scheduling; and
- **Babel owns interpretation:** indexing local snapshots and analyzing their contents.

Dotfiles should invoke Babel rather than implement rclone workflows. Its eventual backup module should be declarative: install the package, render or pipe machine credentials into Babel's documented configuration interface, and schedule `babel archive push`. Babel must accept configuration through a provider-neutral secure file or stdin contract; it must not depend directly on a Bitwarden item name or duplicate the operator's vault implementation.

Recovery must not depend circularly on an already working Babel installation. The dotfiles bootstrap and the external secret authority must remain sufficient to reinstall Babel and recreate its archive configuration without reading the archive. Babel's on-disk configuration remains compatible with direct `rclone crypt` recovery, and a small documented escape hatch must allow an operator with rclone plus the independently stored credentials to list and restore the archive if Babel itself is unavailable. Babel must never be the sole copy of either the credentials or the knowledge required to recover its data.

A local directory is always a valid Babel input. This keeps the analyzer testable and usable independently of the operator-specific deployment.

### 2.4 Primary interaction model

Running `babel` with no arguments opens the primary terminal interface. This is not a secondary viewer layered over a command-line application: it is the intended human product surface, comparable in ambition and polish to `atyrode/code`. Archive setup and health, retrieval, session browsing, analysis selection and progress, recipes, findings, proposals, review, and export must eventually be reachable without leaving the TUI.

Headless subcommands remain required for systemd/launchd, scripting, diagnostics, reproducible tests, and recovery. The TUI and subcommands call the same application services and storage contracts; business logic must not be duplicated in view code.

The implementation should reuse the Bubble Tea and `atyrode/cli-kit` ecosystem used by `code` unless a concrete prototype identifies a blocker. “Beautiful” means a coherent palette and typography, clear focus and selection states, useful empty/loading/error states, responsive layouts from an 80×24 terminal upward, keyboard discoverability, and no loss of meaning when color is unavailable.

## 3. Source data and trust model

Supported sources initially use the archive layout inherited from the existing dotfiles automation:

- OMP: `omp/sessions` and `omp/collab`;
- Codex: `codex/sessions`, `codex/history.jsonl`, `codex/session_index.jsonl`, and `codex/attachments`;
- Claude Code: `claude/projects`.

OMP sessions are project-scoped JSONL event logs under an encoded workspace directory. A session can also have a same-named sibling directory containing side-channel artifacts such as advisor events and tool logs. Adapters must preserve this relationship.

All archive content is untrusted data. A transcript can contain malicious instructions copied from issues, web pages, repositories, tool output, or prior agents. Babel and its model prompts must never interpret transcript text as instructions to Babel. Source content is quoted evidence only.

The archive can also contain secrets, private source code, personal data, and attachments. Therefore:

1. ingestion and deterministic secret preflight happen locally;
2. local model execution is the default analysis mode;
3. hosted-model use requires an explicit per-run choice and a visible disclosure of what leaves the machine;
4. likely secrets are redacted before hosted inference, while local evidence retains references to the original locations;
5. exports redact secret values by default; and
6. logs must not contain raw transcript bodies or credentials.

## 4. Conceptual model

Babel distinguishes four layers so that guesses never become facts merely through repetition.

### 4.1 Source record

An immutable, normalized event or artifact with:

- source kind and adapter version;
- host, workspace, session, and event identifiers where available;
- source path and content digest;
- timestamp and participant/tool role;
- normalized text or artifact metadata; and
- a locator capable of recovering the original evidence.

### 4.2 Observation

A recipe's single-session or single-event claim. An observation includes evidence locators, category, confidence, impact, and recipe provenance. It cannot exist without evidence.

### 4.3 Finding

One or more related observations consolidated across sessions. A finding explains the pattern, counter-evidence, recurrence, affected scope, and why it matters. Findings are deduplicated but retain all supporting observations.

### 4.4 Proposal

A human-reviewable improvement suggested by one or more findings. A proposal contains:

- a concise problem statement;
- the proposed change and expected benefit;
- evidence and recurrence count;
- confidence, impact, and estimated scope;
- a possible target repository or a repository-independent destination;
- risks, counter-evidence, and unresolved questions;
- suggested verification criteria; and
- review status: `new`, `accepted`, `rejected`, `deferred`, or `duplicate`.

A proposal is not an issue and has no external side effect.

## 5. Analysis cookbook

The cookbook is a first-class, versioned part of Babel. Each recipe is a reviewable Markdown document with small machine-readable front matter:

```yaml
id: interaction-friction
version: 1
title: Interaction friction and preventable rework
scope: session
stage: analyze
default: true
```

The body defines:

- the question the recipe answers;
- what counts and does not count;
- required evidence;
- useful counter-evidence;
- classification guidance;
- unsafe interpretations to avoid; and
- examples of strong and weak findings.

Recipes emit the common observation schema rather than inventing incompatible outputs. Changing a recipe's semantic behavior requires incrementing its version so affected material can be reanalyzed.

### 5.1 Initial recipe set

1. **Credential and sensitive-data exposure** — leaked or unnecessarily surfaced secrets, risky handling, and evidence requiring redaction.
2. **Security and operational decision review** — unsafe decisions, missing threat considerations, destructive commands, and weak recovery paths.
3. **Unresolved bugs and critical failures** — failures discovered in discussion but not convincingly fixed or verified.
4. **Agent mistakes and avoidable rework** — wrong assumptions, ignored constraints, symptom suppression, incomplete migrations, and unsupported claims.
5. **Interaction friction** — repeated clarification, ambiguous prompts, missing context, premature questions, or feedback that could become better standing instructions.
6. **Preferences and durable conventions** — recurring operator choices that should become explicit rules, defaults, or documentation.
7. **Reusable processes and skill candidates** — successful multi-step methods worth codifying into a skill, checklist, or runbook.
8. **Automation and tool gaps** — repeated manual work, missing observability, or unavailable capabilities that merit tooling.
9. **Effective patterns** — interactions, verification strategies, and agent behaviors that consistently produced strong outcomes.
10. **Cross-session recurring themes** — repeated findings that become materially more important when viewed across the corpus.

The recipes intentionally include both problems and successes. A system trained only on failures would lose the operator's strongest practices.

## 6. Processing pipeline

### 6.1 Archive and fetch

`babel archive push` copies supported local session sources into the encrypted, host-scoped archive. Upload is append/update-only: it uses `copy`/`copyto`, never `sync`, and does not delete a remote object as an incidental consequence of local state.

`babel archive catalog` performs a read-only remote inventory and does not materialize transcript bodies. The current archive contains the raw session tree and no per-session manifest, so its guaranteed lightweight fields are limited to the host prefix, decrypted archive path, filename-derived timestamp/session identifier, remote modification time, and object size. Title and recorded `cwd` require either a separately verified bounded read, a future manifest, or an explicit full-session pull. Babel must show those fields as unavailable rather than silently downloading an object to fill them.

`babel archive pull --session ID` explicitly materializes selected session objects into a local snapshot. Pull never writes to the remote and records its source host, archive reference, contract version, digest, and fetch time. Status and verification commands expose remote reachability and round-trip integrity without requiring the analysis pipeline.

The first implementation must remain compatible with the current Cellar bucket, `rclone crypt` settings, host prefixes, and object paths so archive ownership can move without migrating stored data.

### 6.2 Ingest

Discover supported session formats, parse them through versioned adapters, associate side-channel artifacts, normalize events, compute digests, and update the local index. Unknown event types are preserved as opaque records and reported rather than discarded.

### 6.3 Preflight

Run deterministic checks before model inference:

- likely-secret and high-risk-data detection;
- malformed or truncated session detection;
- transcript size and attachment inventory;
- duplicate and changed-session detection; and
- a disclosure preview for any hosted-model run.

Preflight findings use the same evidence model as AI-assisted findings.

### 6.4 Analyze

Run enabled session-scoped recipes over new or invalidated material. Large sessions are partitioned on semantic event boundaries, not arbitrary byte offsets. Summaries retain locators to the underlying records.

The analyzer must distinguish:

- what the user reported as fact;
- what an agent claimed or inferred;
- what tools actually observed;
- what changed in a repository; and
- what verification was actually run.

This distinction is essential when judging mistakes, unresolved work, and unsupported success claims.

### 6.5 Synthesize

Corpus-scoped recipes consolidate observations, identify recurrence, merge duplicates, surface contradictions, and create or update findings and proposals. Synthesis never drops the evidence chain.

### 6.6 Review and export

The operator reviews proposals, records disposition and notes, and exports selected material as:

- Markdown for direct reading;
- stable JSON for future integrations; and
- GitHub issue-draft Markdown containing title, body, target candidate, and evidence links.

Exporting a draft is allowed. Calling GitHub to publish it is not.

## 7. Incremental behavior

The normal mode analyzes material not yet processed under the current analysis inputs. `--all` means explicitly reconsider the selected corpus; it is not the default.

An analysis result is keyed by at least:

- normalized source digest;
- adapter identity and version;
- recipe identity and version;
- model/provider identity;
- analysis prompt/runtime version; and
- relevant redaction policy version.

A result is invalidated when one of those inputs changes. Because archived JSONL sessions can grow while retaining the same path, path or modification time alone is never sufficient; Babel hashes normalized content.

Review decisions survive reanalysis. New evidence may supersede a finding, but Babel records the relationship rather than silently replacing human history.

## 8. Proposed CLI

Names are provisional. The commands represent product boundaries rather than an implementation commitment.

```text
babel
babel archive configure --from-json FILE|-
babel archive push
babel archive catalog [--host HOST] [--refresh]
babel archive pull --session ID [--destination PATH]
babel archive status [--json]
babel archive verify
babel ingest PATH...
babel preflight [--since TIME] [--session ID]
babel analyze [--new | --all] [--recipe ID] [--local | --hosted PROVIDER]
babel synthesize [--new | --all]
babel review [--status STATUS] [--recipe ID]
babel export [--format markdown|json|issue-drafts] [--status STATUS] OUTPUT
babel status [--json]
babel run [PATH...] [analysis selection flags]
```

Behavioral rules:

- `babel run` is only orchestration for explicitly selected archive pulls plus ingest/preflight/analyze/synthesize; it does not upload sessions or materialize the entire corpus;
- archive uploads are always explicit through `babel archive push` or an external scheduler;
- archive commands never require the analysis subsystem to be configured;
- no command publishes, edits, or remediates external systems;
- destructive local operations, if later introduced, require an explicit command and are never part of `run`;
- machine-readable output goes to stdout and diagnostics to stderr;
- commands support selection by host, workspace, time range, session, source kind, and recipe; and
- interrupted runs are resumable without duplicating observations.

Bare `babel` is the primary interactive interface. `review` and the other headless commands expose the same capabilities for automation; the storage and command contracts never depend on the TUI being active.

### 8.1 TUI information architecture

The mature TUI has five product areas:

1. **Home** — archive configuration and health, last successful push/pull, indexed-session counts, pending analysis, and recent findings;
2. **Sessions** — searchable, sortable, filterable inventory of available conversations;
3. **Recipes** — enabled analyses, versions, coverage, and evaluation quality;
4. **Findings** — observations and consolidated evidence-backed patterns; and
5. **Proposals** — human review, disposition, targeting, and export.

The first prototype implements only Home, Sessions, and a metadata-only Session detail view. It is deliberately analysis-free: its purpose is to prove the public package, TUI foundation, encrypted retrieval, OMP adapter, local index, and failure behavior as one end-to-end vertical slice.

On an empty first run, Home explains the difference between remote listing metadata and decrypted transcript content and offers an explicit **Refresh catalog** action. Catalog refresh is read-only; launching Babel never silently materializes the corpus or promises fields the current archive does not contain separately. During refresh the TUI shows the current host/path, object progress, cancellation, and actionable errors. If the remote is unavailable, the last complete catalog remains browsable and is clearly marked as cached.

The initial Sessions table sorts newest known activity first and has columns for:

- session title;
- timestamp;
- workspace/folder;
- source machine;
- source kind (`omp` initially);
- session identifier;
- stable archive reference;
- remote size; and
- local state such as remote-only, fetched, changed remotely, or ready for analysis.

The columns are stable, but their values reflect provenance honestly. Before a session is fetched, the existing archive guarantees machine, source kind, archive reference, size, filename-derived timestamp/session identifier, and an encoded workspace component. Title and recorded `cwd` display as unavailable unless a manifest or a separately verified bounded-read capability supplied them. Fetching the session enriches the row from OMP events.

Search and filters operate only on available values. The detail view distinguishes remote listing facts from fields parsed from a fetched session; it never presents filename inference as transcript metadata.

Titles and workspace paths are potentially sensitive and untrusted. Babel strips control sequences before rendering, provides a one-key privacy mode that masks titles and paths, and omits raw values from logs and catalog exports unless explicitly requested.

Opening an unfetched session presents its known metadata and an explicit **Fetch this session** action with the expected size. Only that action—or a deliberate multi-selection fetch—downloads the decrypted JSONL. Viewing transcript content or starting analysis requires a complete, digest-verified local copy.

Metadata refresh is incremental: unchanged entries are reused, growing sessions are marked changed, remote absence does not imply deletion, and refresh never duplicates catalog entries. Phase A does not depend on unverified range-read behavior. If a later experiment proves bounded OMP header reads over the exact rclone crypt/Cellar stack, or archive push begins producing a compact manifest, either can enrich remote-only rows without changing this provenance model.

## 9. Local state and reproducibility

Babel keeps mutable state outside the source repository, following XDG paths by default:

- cache: fetched snapshots and disposable model inputs;
- state: SQLite index, run receipts, review dispositions, and locks;
- data: retained findings, proposals, and exports when no output path is given.

The exact paths and schema are implementation decisions, but these invariants are required:

- source archives are never modified;
- every derived object records provenance and a schema version;
- writes are atomic where partial state would be misleading;
- one failed recipe does not erase successful independent results;
- runs produce receipts containing selection, versions, counts, failures, and disclosure mode; and
- a JSON export plus recipe revision is sufficient to audit why a proposal exists.

## 10. Quality requirements

A useful Babel result must be:

- **grounded:** every factual claim has recoverable evidence;
- **specific:** it describes an observable problem or improvement, not generic advice;
- **deduplicated:** recurrence strengthens one finding rather than flooding the review queue;
- **balanced:** counter-evidence and successful outcomes are visible;
- **actionable:** proposals name a concrete change and verification criteria;
- **calibrated:** confidence is not used as a substitute for evidence; and
- **private by construction:** model disclosure and redaction are explicit.

Initial evaluation should use a small, manually labeled set of real sessions. For each recipe, compare expected observations to output and record false positives, missed findings, unsupported claims, duplicate rate, and evidence quality. A recipe should not join the default set merely because its prose sounds useful.

The TUI is evaluated as an actual terminal surface, not from source structure alone. Its acceptance checks cover narrow and wide terminal layouts, keyboard-only navigation, focus visibility, empty/loading/progress/error/cached states, stable rendering of long titles and paths, and responsive interaction over the real expected session count.

## 11. Failure behavior

- An unavailable archive or model fails the affected stage without corrupting prior state.
- A failed or cancelled pull leaves the last complete local catalog usable and visibly marked as cached or stale.
- Unsupported formats are inventoried and reported.
- Parse errors retain source paths and safe diagnostics but not raw secrets.
- A hosted run is refused if disclosure preview or redaction cannot complete.
- Findings without valid evidence locators are rejected at the schema boundary.
- Partial corpus coverage is prominent in receipts and reports; Babel must not present it as corpus-wide analysis.

## 12. Delivery sequence

### Phase A: prove the archive-backed product shell

- publish and package a runnable public Babel binary;
- launch the primary TUI from bare `babel`, using Bubble Tea and `atyrode/cli-kit`;
- reuse the existing rclone crypt configuration and remote layout without migrating data;
- refresh a read-only catalog from the fields actually available in remote listings, without materializing transcript bodies;
- show title/workspace columns as unavailable for remote-only entries instead of assuming the current archive has a manifest or cheap range-readable metadata;
- present Home, a searchable/filterable Sessions table, privacy mode, and metadata-only Session detail;
- explicitly fetch and digest-verify one selected session, enrich its title and recorded `cwd`, and leave the rest remote-only;
- support incremental catalog refresh, cancellation, offline cached browsing, and honest degraded/partial-failure states;
- expose the same archive catalog/pull/status services through headless commands; and
- visually verify the real TUI across representative terminal sizes and states.

This phase contains no model inference and produces no findings. It proves that Babel can securely retrieve, understand, and present the corpus it will later analyze.

### Phase B: prove the evidence loop

- index normalized OMP events while preserving provenance;
- implement deterministic sensitive-data preflight;
- implement one local-model recipe and the common observation schema;
- synthesize findings and export Markdown/JSON;
- surface analysis selection, progress, findings, and evidence in the TUI; and
- manually evaluate results on a small labeled corpus.

### Phase C: make it operational

- complete the archive subsystem against the existing remote layout and crypt configuration;
- verify byte-identical restore, idempotent upload, append-only behavior, and the direct-rclone recovery path;
- replace the dotfiles-owned upload script with declarative installation, credential delivery, and scheduling of `babel archive push`;
- add incremental analysis invalidation and resumable runs;
- add the initial recipe set and proposal review;
- add issue-draft export; and
- add Codex and Claude Code adapters.

### Phase D: improve the feedback system

- add corpus-level recurrence and contradiction analysis;
- measure recipe precision, misses, and evidence quality over time;
- help turn accepted proposals into recipe refinements; and
- support comparing interaction quality before and after an accepted improvement.

No phase grants Babel permission to apply its proposals.

## 13. Decisions recorded by this draft

1. Babel is an analyzer and recommender, not an actor.
2. Babel's source and distributable package are public; archives, credentials, local state, findings, and model inputs remain private.
3. Bare `babel` opens the primary terminal interface, and the complete interactive product is reachable there.
4. Headless commands and the TUI share one application layer; scheduling and recovery never require an interactive terminal.
5. Babel owns the portable archive contract, encryption/upload/download behavior, retention semantics, and restore CLI.
6. Dotfiles owns Babel's declarative installation, host enablement, credential delivery, and scheduling.
7. Recovery remains possible from dotfiles bootstrap, the external secret authority, and direct rclone without a working Babel installation.
8. The first prototype is an analysis-free vertical slice: inventory the fields the current remote archive actually exposes, mark unavailable metadata honestly, and fetch decrypted JSONL only for sessions the operator explicitly selects.
9. Local directories remain a first-class input.
10. Raw transcripts are untrusted and private; local inference is the default.
11. Analysis is recipe-based, versioned, evidence-constrained, and incremental.
12. The default analyzes new or invalidated material; full reanalysis is explicit.
13. Outputs distinguish observations, findings, and proposals.
14. Proposals may target repositories but are never published automatically.
15. Both harmful and effective interaction patterns are in scope.

## 14. Questions still open

These questions should be resolved through discussion or a narrow prototype rather than guessed in advance:

1. Which local model and structured-output mechanism are reliable enough for the first labeled-corpus evaluation?
2. How much verbatim evidence should appear in normal reports versus being available only through local locators?
3. Should repository targeting be inferred from workspace/remotes, chosen during review, or both?
4. What retention policy should apply to fetched plaintext snapshots and model-ready redacted copies?
5. Should accepted and rejected proposals feed recipe-evaluation fixtures automatically, or only through an explicit curation command?
6. Should Phase A add and backfill a compact per-host manifest so remote-only rows can show title and recorded `cwd`, or accept those fields as unavailable until each session is fetched?
7. Does the exact rclone crypt/Cellar stack support demonstrably bounded header reads, and are their transfer cost and failure semantics preferable to a manifest?
