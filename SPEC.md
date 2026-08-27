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

`babel archive pull` materializes selected host prefixes into a local snapshot. Pull is read-only with respect to the remote and records its source host, remote path, archive contract version, and fetch time. Status and verification commands expose remote reachability and round-trip integrity without requiring the analysis pipeline.

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
babel archive configure --from-json FILE|-
babel archive push
babel archive pull [--host HOST] [--destination PATH]
babel archive list [--host HOST]
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

- `babel run` is only orchestration for archive pull/ingest/preflight/analyze/synthesize; it does not upload sessions or hide a distinct workflow;
- archive uploads are always explicit through `babel archive push` or an external scheduler;
- archive commands never require the analysis subsystem to be configured;
- no command publishes, edits, or remediates external systems;
- destructive local operations, if later introduced, require an explicit command and are never part of `run`;
- machine-readable output goes to stdout and diagnostics to stderr;
- commands support selection by host, workspace, time range, session, source kind, and recipe; and
- interrupted runs are resumable without duplicating observations.

`review` may begin as a line-oriented CLI and later become a TUI. The storage and command contracts must not depend on a TUI existing.

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

## 11. Failure behavior

- An unavailable archive or model fails the affected stage without corrupting prior state.
- Unsupported formats are inventoried and reported.
- Parse errors retain source paths and safe diagnostics but not raw secrets.
- A hosted run is refused if disclosure preview or redaction cannot complete.
- Findings without valid evidence locators are rejected at the schema boundary.
- Partial corpus coverage is prominent in receipts and reports; Babel must not present it as corpus-wide analysis.

## 12. Delivery sequence

### Phase A: prove the evidence loop

- ingest OMP JSONL from a local directory;
- index sessions and preserve event provenance;
- implement deterministic sensitive-data preflight;
- implement one local-model recipe and the common observation schema;
- synthesize findings and export Markdown/JSON;
- manually evaluate results on a small labeled corpus.

### Phase B: make it operational

- implement the archive subsystem against the existing remote layout and crypt configuration;
- verify byte-identical restore, idempotent upload, append-only behavior, and the direct-rclone recovery path;
- replace the dotfiles-owned upload script with declarative installation, credential delivery, and scheduling of `babel archive push`;
- add incremental invalidation and resumable runs;
- add the initial recipe set;
- add proposal review state and issue-draft export;
- add Codex and Claude Code adapters.

### Phase C: improve the feedback system

- add corpus-level recurrence and contradiction analysis;
- measure recipe precision, misses, and evidence quality over time;
- help turn accepted proposals into recipe refinements;
- support comparing interaction quality before and after an accepted improvement.

No phase grants Babel permission to apply its proposals.

## 13. Decisions recorded by this draft

1. Babel is an analyzer and recommender, not an actor.
2. Babel owns the portable archive contract, encryption/upload/download behavior, retention semantics, and restore CLI.
3. Dotfiles owns Babel's declarative installation, host enablement, credential delivery, and scheduling.
4. Recovery remains possible from dotfiles bootstrap, the external secret authority, and direct rclone without a working Babel installation.
5. Local directories remain a first-class input.
6. Raw transcripts are untrusted and private; local inference is the default.
7. Analysis is recipe-based, versioned, evidence-constrained, and incremental.
8. The default analyzes new or invalidated material; full reanalysis is explicit.
9. Outputs distinguish observations, findings, and proposals.
10. Proposals may target repositories but are never published automatically.
11. Both harmful and effective interaction patterns are in scope.
12. The first implementation should prove one narrow end-to-end evidence loop before expanding adapters or recipes.

## 14. Questions still open

These questions should be resolved through discussion or a narrow prototype rather than guessed in advance:

1. Should the first implementation support only OMP, or include all three archived agent formats from its first usable release?
2. Which local model and structured-output mechanism are reliable enough for the first labeled-corpus evaluation?
3. How much verbatim evidence should appear in normal reports versus being available only through local locators?
4. Should repository targeting be inferred from workspace/remotes, chosen during review, or both?
5. Is a terminal review UI valuable early, or are Markdown plus JSON sufficient until analysis quality is proven?
6. What retention policy should apply to fetched plaintext snapshots and model-ready redacted copies?
7. Should accepted and rejected proposals feed recipe-evaluation fixtures automatically, or only through an explicit curation command?
8. How should a private Babel release be installed reproducibly from public dotfiles: an authenticated binary cache, authenticated source access, or an eventually public package?
