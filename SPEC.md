# Babel specification

Status: audited development baseline, revised 2026-08-27: archival is delegated to restic (operator decision); the bespoke `babel/v1` object contract is retired. Phase A local coding may proceed against local-path repositories; the first shared deployment remains gated by §14.


Module path: `github.com/atyrode/babel`.
## 1. Purpose

Babel is an open-ended exploratory instrument for archived conversations between an operator and coding agents. It helps ideas emerge about:

- the operator's systems and repositories;
- the way the operator communicates and collaborates with agents;
- agent instructions, rules, skills, and reusable processes;
- missing tools, documentation, comprehension layers, and automation;
- product, code, security, operational, and human-interface opportunities;
- effective patterns worth preserving and repeating; and
- Babel's own code, cookbook, retrieval, analysis, and interaction design.

The project closes a creative feedback loop:

> conversations and reality produce hypotheses; exploration connects and challenges them; human review decides what becomes useful; the resulting feedback can improve systems, future interactions, and Babel itself.

Babel does not promise reliable, exhaustive, or objectively correct analytical output. Its hard guarantees concern archive integrity, containment, provenance, reproducibility, and no mutating or publishing external effects. Brokered reads are observable external effects and are recorded. Hypotheses, findings, and proposals remain creative, fallible, incomplete interpretations for human review.

## 2. Product boundary

### 2.1 Babel owns

- archival orchestration: source roots, snapshot cadence, stable host identity, tags, and the never-delete retention policy;
- repository configuration and append-only backup/read-only retrieval behavior (repository encryption itself belongs to restic);
- archive status, integrity verification, and recovery-compatible restore commands;
- read-only ingestion of materialized chat archives;
- format adapters for supported agent session formats;
- normalization, indexing, deduplication, and provenance;
- deterministic preflight checks such as likely-secret detection;
- a versioned cookbook of shared investigation policies, optional domain lenses, and meta-analysis recipes;
- open-ended, incremental exploration with a durable hypothesis frontier and cross-session/repository synthesis;
- provenance-bearing findings and proposal generation without claiming analytical correctness;
- a self-hosted loopback web interface as the primary management and exploration surface, plus a minimal terminal status overview and headless commands for operations; and
- globally committed review/refinement/output state plus rebuildable private local caches and materializations.

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

The pre-Babel dotfiles automation is a proven operational prototype, not Babel's compatibility contract. It archives OMP, Codex, and Claude Code trees hourly to a Clever Cloud Cellar bucket through `rclone crypt`, but lacks the session-aware catalog, integrity verification, deduplicated growing-session storage, and selective restore Babel requires.

Babel starts a clean restic repository in the same Cellar account, under its own prefix:

```text
s3:<cellar-endpoint>/<bucket>/babel/restic
```

It ignores the legacy remote namespace entirely. Legacy objects remain untouched and recoverable with direct rclone, but Babel does not list, import, migrate, or preserve their layout. Source data still present on managed machines is captured by Babel's first restic push. The existing backup job remains enabled until a real Cellar round trip covers all three harnesses and the replacement timer is verified; retirement is then a clean dotfiles cutover, not an in-place migration.

Ownership follows dependency direction:

- **Babel owns orchestration:** provider-neutral configuration, source adapters, snapshot cadence/tagging/host identity, the session catalog, shared coordination, append-only retention policy, selective restore, status/integrity commands, and durable analysis/Reality/review state;
- **restic owns the archive repository:** encryption, content-defined deduplication, snapshot format, integrity checking, and S3 transport (the operator deployment uses restic's native S3 backend against Cellar);
- **PostgreSQL owns shared structure and coordination:** deployment/instance/host identity, the session/snapshot catalog, idempotency, leases/fencing, and—beginning in Phase B—client-side-encrypted hypotheses, Reality, questions, outputs, review, lineage, runs, and receipts;
- **the archive adapter owns transport:** Babel invokes the external `restic` binary through a narrow injected port—init, backup, snapshots, check, dump, restore—and never runs `forget` or `prune`; a local-path repository supports fixtures, offline development, and recovery. Phase B evidence, diffs, large outputs, and exports are separate encrypted S3 objects outside the restic repository;
- **dotfiles owns machine convergence:** commit-pinned Nix installation, stable host and instance IDs, secret retrieval, piping a versioned storage document into Babel, and an hourly user timer after manual bootstrap acceptance; and
- **Babel owns interpretation:** cataloging, normalizing, analyzing, and reviewing archived conversations.

The first operator deployment uses the existing Clever Cloud Cellar account and a Clever Cloud managed PostgreSQL database, while the protocol remains portable to compatible S3 and PostgreSQL services. `$XDG_CONFIG_HOME/babel/storage.json` is one mode-0600 provider-neutral document describing `local` or `shared` mode, deployment/instance identity, the restic repository locator, PostgreSQL/TLS settings, and external secret/key references. Babel never accepts credential-bearing URLs on argv, implements repository encryption itself, knows a Bitwarden item name, or invokes Bitwarden. Standalone installations generate stable IDs when dotfiles does not supply them.

Managed setup is a one-way secret handoff, not a Babel-to-Bitwarden integration. During an explicit dotfiles activation/bootstrap, the operator unlocks Bitwarden; dotfiles retrieves common deployment material and the shared restic repository password plus the machine-specific PostgreSQL application credential, combines them with stable host/instance IDs, and pipes the versioned document to `babel storage configure --from-json -`. Babel never receives vault authority or item names.

Secrets never enter Nix derivations or `/nix/store`, argv, shell history, broad process environment, logs, or persistent temporary files. Babel validates endpoints, TLS, identity, schema compatibility, and credentials before atomically replacing `storage.json`; failure preserves the previous valid configuration and prior timer state. The distinct migration credential is retrieved ephemerally only on the designated bootstrap/migration machine and is never persisted in ordinary instance configuration. After health and bootstrap gates pass, dotfiles enables the hourly timer and relocks the vault. Rotation repeats the same atomic flow; per-instance PostgreSQL credentials allow revocation without rotating the fleet.

`local` mode uses a local-path restic repository plus SQLite and is explicitly single-instance development/recovery state. The first deployed v1 uses `shared` mode. Recovery must not depend circularly on Babel or PostgreSQL: dotfiles plus the external secret authority can reinstall/reconfigure Babel, the restic binary alone can restore every archived source tree, and the Phase A PostgreSQL catalog is rebuildable from the repository snapshot list plus rescans of live or restored sources. Babel is never the sole copy of credentials, keys, or recovery knowledge. Loss of the repository password is loss of the archive, so password custody and backup are gated operational controls (§14).

### 2.4 Primary interaction model

The web interface is Babel's primary interactive surface (operator decision 2026-08-28): `babel web` serves a loopback-only, token-guarded browser application owning session browsing and search, transcript viewing, session detail with artifact/blob closure and completeness reasons, archive status, verify, and fetch, and — in Phase B — relationship graphs, evidence/counter-evidence inspection, Reality Ledger and Question workflows, proposal refinement, diffs, output previews, and clean/chaos comparisons.

The terminal stays deliberately minimal: bare `babel` prints a fast offline status overview (build identity, storage configuration, cached catalog size, and the web pointer), and a richer TUI is deferred until the web surface settles rather than being built in parallel. Headless subcommands remain required for systemd/launchd, scripting, diagnostics, reproducible tests, and recovery.

CLI and web call the same Go application services and storage contracts; the web API is served by the same process over the same command implementations, so business logic, sanitization, and authorization are never reimplemented in view code, and the browser never connects directly to PostgreSQL, SQLite, Cellar, restic, Code, OMP, or providers. The TypeScript/React frontend is compiled and embedded into the static Go binary with no runtime CDN or external asset dependency.

### 2.5 Cross-machine continuation boundary

Babel enables a future `code` integration for continuing an archived OMP conversation on another machine, but two different promises must remain separate:

1. **Transcript continuation** is feasible: fetch a complete, immutable session snapshot, validate it, create a new local OMP fork with lineage to the source, and continue the conversation in a chosen workspace.
2. **Exact runtime/workspace resume** is not guaranteed: a transcript does not contain the source process tree, working tree, uncommitted files, credentials, provider sessions, installed tools, or complete machine state.

The product language is **Continue here**, not seamless cloud resume. The safe default creates a new local session identity through OMP's supported fork/import behavior. Babel's fetched cache remains immutable and is never passed to OMP as a file it may append to. Continuing the same mutable identity on two machines is out of scope until a real synchronization protocol provides a canonical writer, leases, versioning, conflict handling, and faster publication than periodic backup.

Responsibility remains layered:

- **Babel** exposes a read-only archived-session locator, fetch, integrity, provenance, and capture-compatibility API;
- **Code** presents Cloud Sessions, chooses a target repository/worktree and routing profile, explains compatibility warnings, and invokes OMP; and
- **OMP** parses the session, reconstructs supported conversation/runtime state, and creates the new local fork.

Neither Code nor OMP reads Babel's private database directly. The integration uses a stable JSON command contract. Babel never chooses a workspace or launches an agent.

Before Code offers **Continue here**, the combined Babel/Code preflight reports:

- session format/version validity and a complete, digest-verified JSONL snapshot;
- completeness of the sibling artifact tree and every referenced OMP blob;
- source host, archive time, session modification time, and whether the snapshot may be stale or from a still-active source;
- recorded `cwd`, additional directories, and any available repository remote/commit/branch/dirty-state fingerprint;
- whether a compatible target workspace exists locally or must be selected/re-rooted;
- whether the installed OMP can read the format; and
- whether recorded models/providers are available under current-machine configuration, without copying credentials from the source machine.

Preflight results distinguish `ready-to-fork`, `needs-target-workspace`, `incomplete-capture`, `workspace-mismatch`, and `stale-or-possibly-active`. Warnings are evidence, not blockers hidden behind a generic score. Exact process/tool-context compatibility is never claimed.

### 2.6 Analysis execution boundary

Babel owns the analysis control plane—recipes, source selection, normalization, discovery, retrieval, batching, sandbox policy, job supervision, output validation, hypothesis lineage, evidence, synthesis, receipts, and review. Code remains the sole owner of provider/model/thinking profiles and launches OMP as the analysis worker.

OMP is the investigator, not the source-format boundary. It can analyze normalized material from OMP, Codex, and Claude Code and may generate arbitrary hypotheses from any operator-approved corpus or repository scope. Broad discovery has an open hypothesis space but not ambient machine authority: each run fixes the allowed hosts, time range, sessions, repository snapshots, capabilities, and disclosure class before work starts. Resource limits bound each sandbox lease, while the durable frontier lets exploration resume indefinitely without changing what ideas are permitted.

An analysis job separates the model-control process from its disposable execution sandbox. Code resolves the profile and gives the supervised OMP controller only the provider transport and credentials required for inference; it exposes no host files or general-purpose tools. Provider secrets and transport are never forwarded to model-visible tool arguments, the evidence broker, repository processes, or the execution sandbox.

All filesystem and command experimentation occurs inside an ephemeral writable sandbox. It may alter a disposable repository clone, compile code, run bounded commands and tests, and create experiments, but it receives no host home directory, SSH agent, secret store, credential files, Docker socket, provider credential, or writable host mount. Approved inputs are mounted read-only; scratch state is discarded after the receipt is finalized. CPU, memory, process, disk, output, and wall-clock safety limits are mandatory, and cancellation terminates the entire process tree.

The execution sandbox has no direct network access. OMP can reach only Code's scoped model transport and Babel's versioned, capability-gated evidence APIs. Babel brokers corpus search/retrieval, repository snapshot materialization, and public research. After private context is available, the broker makes no arbitrary model-controlled request: URL, query, header, body, and redirect fields are disclosure sinks. Requests use validated templates or opaque result IDs plus explicit declassification/consent; userinfo, fragments, arbitrary headers or bodies, private locators, and encoded secrets are forbidden. The broker may search/fetch public material and materialize a public repository at a pinned commit without authentication; it rejects private/link-local destinations and unsafe redirects, limits response types and sizes, and returns source URL, retrieval time, redirect chain, and content digest. Remote content and repository code remain untrusted evidence. Private remote-repository access is out of scope for v1; an explicitly approved local snapshot may still contain private code. The exact broker protocol is a Phase B acceptance gate.

Repository commands execute only inside the sandbox against a pinned disposable snapshot. A run receipt records its source locator, commit and dirty-state fingerprint where available, every evidence-tool request, command, exit status, bounded output digest, generated diff, research source, and capability decision. No analysis capability can push, publish, mutate a source repository, or write outside the sandbox.

Code's versioned profile-configuration and sandboxed-analysis-worker capabilities, including their capability/version handshake, are a Phase B gate. Once accepted, the configuration-only mode opens Code's existing dials, saves the result under Code's ownership, returns a stable profile ID/revision plus non-secret privacy/cost/capability metadata, and exits without launching OMP; the worker mode accepts a Babel job over stdin, resolves the named profile, starts the credential-isolated OMP controller plus disposable execution sandbox, connects only authorized evidence/tool capabilities, and streams versioned structured progress/tool/result events over stdout.

Babel stores only the Code profile reference, approved guards, capability grant, and resolved non-secret execution metadata in run receipts. One selected Code profile applies to every recipe in a run. Babel keeps its TUI responsive while owning cancellation, sandbox and process-tree lifetime, tool authorization, output validation, and final exit status; analysis is never detached.

Preparation is automatic; inference is explicit by default. Archive refresh, manifest ingestion, deduplication, normalization, deterministic preflight, and local index maintenance may run unattended. A model run starts only through an explicit operator action unless scheduled inference has been separately enabled. Scheduled inference names a saved Code profile and an approved source/capability/disclosure/cost envelope; only its material-change fingerprint remains scheduled-inference design material until the Phase B handshake gate is resolved.

The runtime dependency is directional per operation: Babel calls Code only for analysis; Code calls Babel only for Cloud Sessions list/inspect/fetch. Neither integration calls back into the other. Code's Cloud Sessions continuation integration remains OMP-specific because it launches a local OMP fork.

### 2.7 Local web security boundary

`babel web` starts only on explicit request, binds an ephemeral loopback port, and stops on operator action or process exit. V1 has no LAN, remote-browser, or persistent-listener mode. Remote access is a separate future authentication/transport design, never a bind-address flag.

Each launch creates a 256-bit one-time bootstrap nonce. `babel web --open` places it only in the URL fragment of the loopback bootstrap page; fragments are never sent in HTTP requests. Embedded bootstrap code immediately removes the fragment with `history.replaceState`, posts the nonce in a request body under `Referrer-Policy: no-referrer`, and receives a rotated host-only `HttpOnly; SameSite=Strict` session cookie. The nonce is single-use and expires quickly; lock/stop revokes every nonce and session. No bearer credential, transcript content, or sensitive selector appears in a query string, request log, referrer, or retained history entry.

All archive, transcript, repository, model, research, Reality Ledger, and output content is untrusted. React rendering escapes text by default; any Markdown/diff renderer uses an allowlisted AST with raw HTML, active SVG, scriptable URLs, and unsafe schemes disabled. Web responses and browser-visible errors follow the same secrecy/redaction contracts as terminal surfaces. **Lock and stop server** invalidates the launch session and terminates the listener.

## 3. Source data and trust model

Babel's core is harness-agnostic. OMP, Codex, and Claude Code implement source adapters over one metadata, normalized-event, provenance, hypothesis, observation, finding, and proposal model.

The v1 archive covers all three from its first release:

- **OMP:** sessions, collaboration data, sibling session artifacts, and the content-addressed `~/.omp/agent/blobs` closure required by persisted blob references;
- **Codex:** sessions, `history.jsonl`, `session_index.jsonl`, and attachments; and
- **Claude Code:** project/session trees and their referenced local artifacts where the on-disk format exposes them.

Each adapter must always preserve and selectively retrieve the raw chat logs. OMP is the reference and highest-fidelity adapter. Codex and Claude Code metadata extraction is best effort where formats are undocumented, unstable, or incomplete, but inability to derive a title, workspace, lifecycle state, or artifact closure never excludes the raw transcript from backup or later analysis. Every catalog row records adapter version and metadata-completeness flags instead of pretending parity.

The catalog separates a portable common shape from versioned adapter metadata. Required common fields are `harness`, `adapter_schema`, stable host ID and display name, adapter-defined source/session identity, and description time. Historical captures are addressed by restic snapshot ID plus the session's source paths, not by bespoke revision keys. Common catalog fields—title, workspace/project, creation and modification times, lifecycle state, and repository fingerprint—are nullable. Missing values remain `null` and set explicit completeness reasons; adapters never synthesize values merely to satisfy a shared shape.

Each row also contains a namespaced `adapter_metadata` object whose schema version is recorded independently. Babel preserves unknown extension fields while reading a compatible catalog row. V1 adapter guarantees are deliberately unequal:

- **OMP:** raw session JSONL, sibling collaboration/artifact data, and the complete set of resolvable referenced blobs; unresolved references are listed and force `continuation_grade=false`;
- **Codex:** raw session logs, `history.jsonl`, `session_index.jsonl`, and discovered referenced attachments, with title/workspace/lifecycle and attachment closure allowed to be unavailable; and
- **Claude Code:** raw project/session logs and discovered referenced artifacts, with project, lifecycle, timestamps beyond filesystem observations, and artifact closure allowed to be unavailable.

The catalog records those common fields, adapter extensions, completeness reasons, and available repository fingerprint. Titles, paths, and adapter extensions never enter PostgreSQL in plaintext (§9) and remain subject to TUI privacy masking; repository bytes are encrypted by restic itself.

All archive content is untrusted data. A transcript can contain malicious instructions copied from issues, web pages, repositories, tool output, or prior agents. Babel and its analysis workers treat transcript text only as quoted evidence, never as instructions.

The archive can contain secrets, private source code, personal data, and attachments. Therefore:

1. ingestion and deterministic secret preflight happen locally;
2. inference discloses the selected Code profile's local/hosted class before material is sent;
3. hosted inference requires explicit per-run consent or a separately authorized schedule;
4. likely secrets are redacted before hosted inference, while local evidence retains locators to the original;
5. exports redact secret values by default; and
6. logs never contain raw transcript bodies or credentials.

The public repository and CI contain only generated synthetic fixtures. Real operator transcripts, titles, paths, catalogs, credentials, and analysis outputs are never committed.

## 4. Conceptual model

Babel distinguishes five layers so that unconstrained ideas can emerge without guesses becoming facts merely through repetition.

### 4.1 Source record

An immutable, normalized event or artifact with:

- source kind and adapter version;
- host, workspace, session, and event identifiers where available;
- source path and content digest;
- timestamp and participant/tool role;
- normalized text or artifact metadata; and
- a locator capable of recovering the original evidence.

### 4.2 Candidate hypothesis

An idea worth investigating, preserved even when it is speculative, uncategorized, duplicated, or not selected within the current run's budget. It records its origin cues, generating or refinement run, parent hypotheses, provisional labels, novelty/priority signals, and status: `untriaged`, `queued`, `investigating`, `deferred`, `rejected`, or `promoted`.

Hypotheses form a durable frontier with typed links such as `derived-from`, `corroborates`, `contradicts`, `supersedes`, and `same-concept`. Sorting never deletes a hypothesis. A candidate may develop only through the path **hypothesis → one or more observations → finding → proposal**; developed hypotheses never skip observations.

### 4.3 Observation

A provenance-bearing claim over session, repository, experiment, or research evidence. An observation includes immutable evidence locators, claim category, confidence, impact, recipe provenance, and explicit counter-evidence or absence thereof. It cannot exist without evidence.

### 4.4 Finding

One or more related observations consolidated across relevant sessions, repositories, experiments, or research sources. A finding explains the pattern, counter-evidence, recurrence where applicable, affected scope, and why it matters. Findings are deduplicated but retain all supporting observations.

### 4.5 Proposal

A proposal is the canonical private review artifact: a human-reviewable possible improvement suggested by one or more findings. It contains:

- a concise title, problem/opportunity statement, and proposed outcome;
- linked hypotheses/findings and their private provenance;
- applicability and temporal status;
- supporting and conflicting material, uncertainty, impact, and estimated scope;
- zero or more suggested target repositories/systems with confidence and rationale;
- risks, unresolved questions, prerequisites, and suggested verification criteria;
- privacy/publication classification;
- zero or more suggested output destinations; and
- review status: `new`, `accepted`, `rejected`, `deferred`, `duplicate`, or `refine-requested`.

A proposal is not an issue, document, or instruction and has no external side effect.

### 4.6 Output projections

After review, Babel can render a proposal as a sanitized GitHub issue draft, cross-system improvement brief, operator note, skill/runbook/instruction draft, investigation brief, effective-pattern note, Babel/cookbook experiment, or private security brief. A proposal may have several or no destinations; rendering never changes its canonical private record.

GitHub issue draft is the primary projection for a bounded repository change. It contains a public-safe, self-contained rationale, proposed outcome, risks, acceptance criteria, material counter-evidence, and uncertainty suitable for the destination audience. Sensitive session/finding locators and excerpts remain private in Babel. Target repository and publication safety are suggestions for operator review, never automatic facts.

### 4.7 Persistent review and refinement

In the managed v1 deployment, hypotheses, observations, findings, proposals, review events, operator context, lineage, refinement requests, and their required evidence are durable, browseable PostgreSQL rows plus encrypted Cellar objects; no committed Babel output is authoritative only on its producing machine. Decisions are append-only disposition events: `accept`, `reject`, `defer`, or `duplicate`; rejection never deletes a record. `Reject and refine` is one atomic operation that appends a `reject` event and creates an authorized distinct refinement request in the same PostgreSQL transaction; there is no standalone `refine` disposition event. Operator context is attributed guidance, not independent evidence.

A refinement run may add sources or context, runs independently of its parent, and creates immutable descendants through `refines`, `responds-to`, `supersedes`, `splits`, or `merges` links; it never overwrites originals. PostgreSQL plus encrypted Cellar objects make its output, evidence, and lineage globally browseable and continuable by any Babel instance, even when host-pinned reruns require a workspace. The CLI and TUI expose review history, lineage, context attribution, refinement requests, and separate refinement-run status.

Every refinement worker receives the refusal/refinement event and attributed reviewer context in its prompt and must emit a structured **durable-learning assessment** before producing descendants. The assessment chooses `none`, `alongside`, or `instead`, with a rationale, intended scope, sensitivity, supporting evidence, and proposed destination. `none` means the correction is specific to this output; `alongside` creates both a revised descendant and a separate lasting-context proposal; `instead` creates no replacement of the rejected output and proposes only the lasting context. Destinations are explicit rather than freeform global memory: a Reality fact/policy plan, cookbook or lens change, skill/runbook/instruction draft, effective-pattern note, or operator note.

The refinement agent may propose but never authorize lasting context. The proposed memory artifact follows its destination's normal evidence and review rules; Reality/entity/focus-policy changes require the existing atomic operator-plan acceptance, and other durable-learning proposals remain reviewable outputs until explicitly accepted. Rejection, assessment, any revised output, any memory proposal, and its eventual disposition retain separate immutable IDs and lineage, so accepting a revision never silently accepts the proposed memory or vice versa.

### 4.8 Reality Ledger, entity identity, and Questions

Babel models non-GitHub reality as a versioned **Reality Ledger**, not freeform model memory. Stable entities represent projects, repositories, machines, services, providers, environments, organizations, and other operator-defined subjects. Each entity has a global ID, typed aliases and relationships, and append-only merge/split history so repository renames, paths, chat terminology, and service moves do not lose identity and mistaken resolutions remain reversible.

A fact is an immutable revision containing subject, predicate, typed value or object entity, `valid_from`/`valid_until`, `observed_at`, provenance locator, authority, confidence, sensitivity, status, and superseded/disputed links. States include `proposed`, `active`, `superseded`, `disputed`, and `stale`. Lifecycle, ownership, and analysis policy remain separate predicates: for example `active|maintenance-only|dormant|retired`, `owned|contributed|external`, and `normal|learn-only|no-code-investigation|excluded`. Lifecycle never silently implies an expenditure policy; explicit versioned focus rules perform that mapping.

Only attributed operator actions and configured trusted sources may authorize facts. Initial v1 sources are operator answers/edits plus versioned provider-neutral JSON inventory import, allowing dotfiles to supply facts it owns such as stable machines, environments, intended service placement, and project/service/host relationships. Credentials are forbidden. Each trusted source declares the predicates/entities it may author; Git activity, conversations, repository inspection, and Babel analysis remain observations or proposed revisions rather than authority.

Facts enter through direct edits, trusted imports, or prioritized **Reality Questions**. Questions acquire missing context, refresh stale facts, resolve source conflicts, resolve entity aliases/merges, set focus policy, clarify ambiguous answers, or fact-check suspected drift. Each durable question records its target entities/predicates, why it was asked, dependent hypotheses/work, existing/conflicting facts, sensitivity, expected authority, and state: `open`, `answered-uninterpreted`, `interpreting`, `plan-ready`, `answered`, `snoozed`, `declined`, `obsolete`, or `superseded`.

Every answer is retained verbatim as an attributed immutable event and sent through a versioned Code→OMP **Answer Interpreter** with the question, relevant context snapshot, conflicts, and provenance. The interpreter emits a structured multi-action plan that may propose fact assertion/supersession/dispute, entity merge/split, focus-policy change, hypothesis creation, a request to investigate an issue-shaped output through the normal evidence pipeline, refinement, follow-up question, or no action. It never creates a proposal that bypasses **hypothesis → observation → finding → proposal**, and it can never publish an issue. If interpretation is unavailable or fails, the raw answer remains `answered-uninterpreted` for retry.

Agent interpretation never silently becomes authoritative reality. Non-authoritative descendants such as hypotheses and follow-up questions may be retained immediately; any fact, entity-resolution, or focus-policy mutation requires one explicit operator acceptance of the displayed plan and commits atomically with the question disposition. The original question, answer, plan, acceptance/rejection, and resulting revisions remain linked. Freeform text is preserved as provenance but is never used as an unparsed global memory prompt.

Facts have predicate-specific freshness: operator intent does not expire automatically, while volatile fleet/deployment observations carry refresh expectations or TTLs. Expiry marks a fact stale rather than deleting it; contradictions create disputes. A prioritized **Reality Inbox** ranks blocking, maintenance, and curiosity questions by affected work, avoided investigation cost, dependency count, staleness, and security/disclosure impact, while deduplication and `declined`/`unknown` outcomes suppress repeats until materially new evidence exists.

Discovery persists hypotheses before context-based focus. After emergence, Babel resolves entities and attaches an immutable as-of/current context snapshot; deterministic policy may defer cloning, testing, research, or repository-specific proposals without deleting the hypothesis. Analysis queries the ledger by entity, relationship, predicate, valid time, freshness, and conflict rather than injecting the entire ledger into every prompt. Retrieval/RAG may find candidate context but never establishes authority. Context-blind controls may measure ledger-induced bias, and the challenger checks stale/disputed facts behind focus decisions.

Phase A's catalog host identity/display history remains the minimal reality substrate. Full entities, facts, questions, answer interpretation, trusted inventory import, and globally durable encrypted Reality Ledger state are Phase B capabilities.

## 5. Analysis cookbook

The cookbook is executable exploratory policy, not a fixed taxonomy or one opaque prompt. It gives analysis productive starting structures while preserving arbitrary emergence. It is versioned in the public repository and has three asset kinds:

- **investigation policies** define shared retrieval, experimentation, challenge, temporal, and synthesis techniques;
- **domain lenses** define useful questions, evidence rubrics, exclusions, and classifications without limiting what discovery may propose; and
- **meta recipes** explore Babel's cookbook, analysis process, prior outputs, and reviewer feedback.

### 5.1 Recipe contract

Each recipe is a reviewable Markdown document with machine-readable front matter:

```yaml
id: outcome-integrity
version: 1
kind: lens
scope: [session, corpus, repository]
stages: [investigate, challenge, synthesize]
capabilities: [corpus-search, repo-read, sandbox-exec]
default: true
```

The body defines the question and why it may be fruitful; inclusion, exclusion, and ambiguity guidance; cues useful when sorting emergent hypotheses; evidence and counter-evidence to seek; temporal and present-reality checks; suggested classifications and stopping conditions; cross-session synthesis keys; capability needs; known failure modes; and examples. These are guidance, not proof that the lens is exhaustive or that complying with it makes an answer correct.

A recipe never selects a provider or model. Babel combines the versioned recipe with a fixed containment/provenance envelope and structured output contracts; transcript, repository, and web content are always delimited as untrusted evidence. Semantic behavior changes require a recipe-version increment.

### 5.2 Open discovery and the hypothesis frontier

Discovery is deliberately divergent. Within the approved evidence and capability boundary, it may emit any candidate hypothesis without first fitting a known lens, category, expected proposal type, evidence threshold, or likelihood score. Every candidate and its origin is persisted. Classification, clustering, deduplication, and priority sorting happen afterward; an uncategorized candidate remains valid, and recurring valuable uncategorized candidates may justify a new lens.

Investigating a hypothesis may emit further hypotheses. Babel adds them to the durable frontier and records their relationships rather than forcing the current job to finish every branch. Finite runs defer the unexplored frontier; they do not erase it. In the unlimited-inference limit, exploration continues until further rounds yield no materially novel candidates, evidence, experiments, or contradictions.

Sorting optimizes operator attention rather than sanitizing ideas. It may estimate novelty, potential value, uncertainty, evidence availability, investigation cost, and similarity to prior reviewed work. Those estimates affect ordering only. Candidates remain browseable with the model's original wording, provenance, and later review outcome.

### 5.3 Experimental chaos runs

Exploration defaults to a `clean` run. An explicitly selected `chaos` run injects unrelated perturbation material during divergent discovery to test whether forced association yields ideas the linked clean control misses. Chaos has its own frontier branch, run receipt, random seed, atom-selection algorithm, and optional reusable chaos pack.

A **chaos atom** is a bounded stimulus with provenance and a declared type. Any immutable revision of any durable Babel output or entity may be selected: hypotheses, observations, findings, proposals, review events and rejection context, refinement requests and outputs, receipts, projections/exports, generated diffs, notes/briefs, cookbook experiments, and prior chaos outputs, as well as archived discussions, public research, or bounded public-repository material. The atom records its exact immutable revision and lineage. Public atoms pass through the research broker. Selection as an atom never authorizes executing its code.

Within a chaos run, `marked` presentation tells the investigator which material is non-evidence stimulus. `blind` presentation withholds why it appeared, but Babel still records every boundary and quarantines the whole branch. Ordinary clean exploration never hides injected context.

A separate Code job may create **synthetic perturbation atoms** from external random seeds or atom combinations. They are recorded as model-generated descendants with their own profile and receipt, not described as true randomness. Model-produced material and any ancestor or descendant reached through recursive reuse remain stimulus only and can never become independent corroboration.

An atom cannot support the hypothesis it induced. Before promotion, a chaos-origin candidate must survive a targeted clean reinvestigation that omits every atom. Evaluation compares novel useful yield, clean-survival rate, false associations, prompt-injection behavior, operator attention, and cost. Chaos defaults off.

### 5.4 Shared investigation techniques

After emergence, an investigator may:

1. search other discussions, prior hypotheses/findings, repository snapshots, Git history, tests, and authorized public research for related concepts;
2. seek corroboration, contradictions, alternative explanations, and older or newer states;
3. distinguish what a conversation claimed from what was observable then and now, using `historical`, `still-applicable`, `resolved`, `regressed`, `contradicted`, or `unverifiable` where useful;
4. modify a disposable clone and run bounded experiments to test an idea;
5. ask a logically separate challenger to falsify or reframe the hypothesis; and
6. synthesize evidence, dissent, uncertainty, and descendant ideas without implying certification.

The challenger is a logically separate job with an intentionally skeptical brief: attack assumptions, search for disconfirming evidence and counterexamples, test whether either the operator or agent made a weak decision, identify opportunity cost, and propose stronger alternatives. It must ground criticism in evidence, consequences, missing checks, or concrete alternatives and must not infer character, ability, emotion, or intent.

The challenger emits objections, counter-evidence, or new hypotheses; it cannot directly create or promote a finding. Before ordinary promotion, a standard challenger pass examines the developed observations. A separate synthesizer then judges the exploration and critique together, preserves unresolved objections, and is instructed to agree with neither side by default. Exploratory candidates may remain unchallenged, but their status makes them ineligible for promotion.

Retrieval is hybrid rather than “vector RAG” by definition. V1 provides provenance-preserving full-text search, structured filters, entity/repository/session links, and temporal filters. Semantic retrieval may be added as another idea/evidence generator after evaluating its privacy, cost, retrieval diversity, and contradiction behavior. Retrieval rank never becomes evidence strength.

Repository and test observations apply only to the pinned snapshot and command environment recorded in the receipt. They can establish behavior in that environment but not infer operator intent. Unavailable reality evidence remains visible as uncertainty rather than being filled from conversational confidence.

### 5.5 Baseline domain lenses

The initial cookbook contains eight useful but non-exhaustive lenses:

1. **Outcome integrity and unresolved state** — compare requested outcomes, agent claims, observed changes, and verification; explore incomplete, falsely closed, regressed, or genuinely resolved work.
2. **Security, privacy, and trust boundaries** — explore concrete exposure, unsafe authority, credential handling, destructive behavior, and missing containment.
3. **Code health, maintainability, documentation, and comprehensibility** — explore dead code, complexity, missing tests/docs, misleading abstractions, absent human comprehension layers, and other improvements against snapshots and history.
4. **Engineering decision quality and operational risk** — revisit assumptions, alternatives, constraints, reversibility, recovery, and measured consequences without treating hindsight as certainty.
5. **Human–agent coordination and avoidable rework** — look for observable ambiguity, ignored constraints, repeated corrections, weak handoffs, comprehension friction, and operator struggle without diagnosing emotion, ability, or mental state.
6. **Durable operator model** — explore preferences, constraints, and standing conventions while distinguishing recurring evidence from one-off instructions and retaining contradictions.
7. **Reusable practice and capability leverage** — explore successful procedures, skill candidates, missing tooling, automation opportunities, and their prerequisites or recurring costs.
8. **Effective patterns and enabling conditions** — preserve strategies that produced strong outcomes, their enabling context, counterexamples, and limits.

Cross-session recurrence is a property available to every lens, not a ninth topic. The lenses organize and inspire what emerges; they do not constrain discovery.

Phase B fully authors and default-enables five initial lens recipes: outcome integrity, security/privacy, code health/comprehensibility, human–agent coordination, and effective patterns. Decision quality, durable operator model, and capability leverage ship as reviewable drafts until corpus evaluation sharpens their overlap and guidance. Open discovery remains enabled independently and may emit hypotheses in any of these areas or none.

### 5.6 Developed hypotheses and findings

A developed hypothesis can record a lens or ad hoc framing, recipe/policy versions, origin and lineage, supporting and conflicting locators, source authority and timestamps, retrieval trace, sandbox/research checks, temporal interpretation, uncertainty, potential value, and the next evidence that could change it. None of those fields turns it into objective truth or substitutes for observations. A finding is created only from one or more observations developed while investigating the hypothesis; “developed enough for focused human review” does not mean “verified correct.”

### 5.7 Babel analyzing Babel

A self-analysis run may explicitly include Babel's pinned repository snapshot, specification, cookbook versions, prior hypothesis frontier, run receipts, resource/tool traces, reviewer outcomes, and evaluation corpora. It may inspect or experimentally modify a disposable Babel clone, run Babel in the sandbox, analyze prior analyses, and propose changes to code, UI, recipes, retrieval, evaluation, or the analysis architecture itself.

Recursive lineage and depth are recorded. A descendant analysis never overwrites its ancestor, and generated material is marked as model-produced so it cannot become independent corroboration through repetition. Self-analysis has the same containment and no-publication boundaries as every other run.

The `cookbook-quality` meta recipe may propose versioned changes or entirely new lenses/policies based on useful uncategorized candidates, reviewer outcomes, misses, duplicates, unsupported claims, retrieval failures, evidence coverage, creativity, cost, and latency. The analyzer never edits or promotes its active cookbook. A human reviews proposed diffs; experiments compare versions on held-out material while preserving previous recipes, hypotheses, and receipts. Optimization favors useful emergence per unit of operator attention, not a false promise of universally reliable answers.

## 6. Processing pipeline

### 6.1 Archive publication

`babel archive push` discovers OMP, Codex, and Claude Code source roots through versioned adapters and backs them up with restic into one shared repository under the machine's stable host identity, tagged `babel`. Babel initializes the repository idempotently, backs up whole source roots—raw session logs, sibling artifacts, and OMP's content-addressed blob store—and reports restic's summary (snapshot ID, files new/changed, bytes added) as the push result. Hourly publication and permanent storage cost scale with the change rate: restic deduplicates content-defined chunks, so a grown session uploads only its new chunks and an unchanged corpus uploads almost nothing.

Captures are crash-consistent per file, not transactional across files (recorded decision). Session logs are append-mostly, so a capture taken mid-write yields a prefix plus at most a torn final line; adapters and normalization tolerate torn or malformed lines by counting and skipping them, and the next hourly snapshot supersedes the capture. OMP's blob store is content-addressed and never rewritten, so closure races cannot corrupt stored blobs. A snapshot's existence never implies a stable continuation-grade capture; continuation-grade claims (§2.5) require adapter-verified closure at read time.

The first successful push is an explicit bootstrap/backfill, not an incremental continuation of the old backup: starting from empty state it backs up every configured local source root in full, including sessions older than Babel's installation. Data that exists only in the ignored legacy remote namespace is not backfilled. The push result reports which adapter roots existed and were captured; the prior dotfiles backup remains enabled until a real Cellar round trip covers all three harnesses.

Retention is append-only. Babel never invokes `restic forget` or `restic prune`, never deletes a snapshot or legacy object, and no remote prune command exists in v1. Concurrent pushes from multiple machines are safe: restic serializes writers with repository locks, and snapshots are per-host by construction. An interrupted backup never publishes a partial snapshot—restic writes the snapshot record last—and a re-run uploads only chunks the repository does not already contain. Distinct from interruption, a backup that completes with unreadable files publishes a visibly incomplete snapshot: restic exits with its incomplete status, push fails loudly while still reporting the summary, per-file diagnostics name the unreadable paths, and the snapshot remains usable for everything it holds.

### 6.2 Catalog and selective fetch

The session catalog is built from live local source trees, not decoded from remote objects: adapters discover and describe sessions—title, workspace, timestamps, repository fingerprint, completeness reasons, artifact/blob closure—on each machine, and shared mode publishes catalog rows to PostgreSQL (within its plaintext allowlist, §9) so every instance can browse the fleet. Historical states are addressed by restic snapshot: a session selector plus an optional snapshot ID (default: the latest snapshot) identifies one immutable capture reproducibly.

`babel sessions fetch SELECTOR [--snapshot ID]` restores the selected session's file closure—primary log, sibling artifacts, resolved blobs—from the chosen snapshot into the private local data store. Today the closure is resolved from live local sources, so fetch covers sessions this machine has (or had at capture time under the same paths). Cross-host fetch is shared-mode work gated in §14: source-file paths never enter PostgreSQL, so a second instance resolves another host's selector by listing the snapshot's own encrypted file tree (`restic ls`) and deriving session identity from those paths with the same deterministic adapter rules used at discovery; the two-instance acceptance in §10 exercises exactly that path. Fetched sessions persist until explicit prune, are never modified, and local prune never touches the repository.

The legacy pre-Babel namespace is ignored. There is no range-read probing or best-effort legacy import in the product path.

### 6.3 Ingest and normalize

Adapters parse fetched raw logs into a common event model while preserving opaque unsupported records and exact source locators. The common layer distinguishes user reports, agent claims, tool observations, repository changes, and verification evidence. Unknown or partial Codex/Claude structures degrade explicitly rather than being discarded.

### 6.4 Deterministic preflight

Before inference, Babel checks likely secrets and high-risk data, malformed/truncated sessions, transcript and attachment size, artifact/blob closure, duplicate/changed inputs, and the selected Code profile's disclosure class. These results use the same evidence model as AI observations.

### 6.5 Explore through Code

The operator selects a scope or explicitly starts broad discovery, chooses one Code profile, and grants capabilities for the run. Babel creates versioned jobs and launches Code's sandboxed OMP worker. The worker may explore approved sources and iteratively request corpus, repository, command/test, and brokered-public-research evidence; Babel authorizes every request and streams structured results with immutable locators.

Discovery persists every candidate before sorting. Babel clusters and links candidates, maps them to known lenses when useful, and maintains a resumable frontier. Resource limits choose what is explored now, not what ideas are permitted to exist. Investigations can recursively add candidates; finite runs checkpoint the remainder.

Babel validates structured events and provenance before storing them, but it does not certify analytical correctness. The receipt records policies/lenses, hypothesis lineage, adapters, Code/profile revision, resolved provider/model/thinking metadata, sandbox and research grants, disclosure mode, source and repository digests, retrieval/tool traces, deferred/rejected candidates, failures, resource use, and timing. A run is durably committed only when its required PostgreSQL rows and encrypted Cellar objects have remotely committed; an outage leaves staged output visibly `pending-sync`, not globally committed, and idempotent sync may later complete it. A failed independent exploration does not erase successful work.

### 6.6 Synthesize

Recipes operating over an explicit preparation scope may consolidate observations across sessions, repositories, experiments, and research evidence, identify recurrence where applicable, deduplicate through links, expose contradictions and counter-evidence, and create immutable findings and proposals without losing provenance.

### 6.7 Review and project

The operator records append-only `accept`, `reject`, `defer`, or `duplicate` events for any reviewable hypothesis, finding, or proposal, with optional attributed context. `Reject and refine` atomically records a `reject` event and creates a distinct authorized refinement request in the same PostgreSQL transaction; it never edits or deletes the rejected entity. Babel preserves the complete private evidence view independently of any output projection.

Phase B exports raw private run, hypothesis, observation, finding, and proposal JSON or Markdown. Phase C rendering creates sanitized destination-specific Markdown or JSON projections. GitHub issue drafts pass through secret/path/private-evidence redaction while retaining public-safe material counter-evidence and uncertainty; security briefs default private-only. Babel may suggest destinations and target repositories, but publishing, copying into an external system, or applying a proposed change occurs outside Babel.

## 7. Incremental behavior

Normal preparation indexes new or changed material. Exploration may start from newly indexed material, an explicit selection, the existing deferred frontier, or a broad-discovery scope. Re-running an unchanged scope is allowed because creative output may differ; Babel never presents cache reuse as semantic equivalence.

Every run records:

- normalized source and capture digests;
- source-adapter identity/version and metadata completeness;
- cookbook policy/lens identities and versions;
- selected frontier roots and prior-hypothesis identities;
- Code version and profile ID/revision;
- resolved provider/model/thinking metadata returned by Code;
- sandbox, tool, repository, and public-research capability versions;
- analysis job/prompt/schema version; and
- redaction/disclosure policy version.

Those inputs make a run reproducible enough to inspect, not deterministic enough to promise identical ideas. Review decisions survive re-exploration; descendants and new evidence link to rather than silently replace prior hypotheses or findings. Managed durability is remote: PostgreSQL and encrypted Cellar jointly preserve every committed run, receipt, evidence object, output, and lineage for browsing and continuation by any Babel instance.

Archive publication, catalog ingestion, and pending-output sync are independently incremental and idempotent. Snapshots are immutable, catalog merges are idempotent, deduplicated chunks are not re-uploaded, and local caches record the exact observed snapshot and sync state.

## 8. Proposed CLI

Names are provisional; behavioral boundaries are not.

```text
babel version --json
babel [--repo LOCATOR] [--password-file FILE]
babel web [--port N] [--open]
babel storage configure --from-json FILE|-
babel storage status [--json]
babel storage migrate --from-json FILE|-
babel archive push [--json]
babel archive status [--json]
babel archive verify [--deep] [--json]
babel sessions list [--harness omp|codex|claude] [--json]
babel sessions inspect SESSION [--json]
babel sessions fetch SESSION [--snapshot ID] [--json]
babel sessions prune --local [selection flags] [--yes]
babel prepare [selection flags]
babel explore --preparation PREPARATION_ID [--new | --all | --resume ID] [--mode clean|chaos] [--chaos-pack ID] [--presentation marked|blind] [--lens ID] [--repo PATH] [--public-research] [--code-profile PROFILE_ID]
babel analysis profile configure
babel analysis profile edit PROFILE_ID
babel hypotheses list [--status STATUS] [--lens ID] [--json]
babel hypotheses inspect HYPOTHESIS_ID [--json]
babel review [--status STATUS] [--lens ID]
babel review decide ENTITY_ID --accept|--reject|--defer|--duplicate|--reject-and-refine [--context CONTEXT_ID]
babel refine REQUEST_ID [--preparation PREPARATION_ID] [--source SOURCE] [--context CONTEXT_ID]
babel export raw ENTITY_ID [--format markdown|json] OUTPUT
babel export projection PROPOSAL_ID --destination issue|brief|operator-note|skill|investigation|pattern|cookbook|security [--format markdown|json] OUTPUT
babel reality entities list [--type TYPE] [--json]
babel reality inspect ENTITY_ID [--as-of TIME] [--json]
babel reality import --source SOURCE_ID --from-json FILE|-
babel reality questions list [--state STATE] [--json]
babel reality questions answer QUESTION_ID --from-file FILE|-
babel reality plans decide PLAN_ID --accept|--reject
babel status [--json]
```

Behavioral rules:

- bare `babel` is a fast offline status overview; `babel web` serves the primary browser surface on 127.0.0.1 only, guarded by a per-launch random token, with archive actions limited to the same read/verify/fetch surface the CLI exposes;
- `--repo LOCATOR --password-file FILE` provides an ad-hoc single-instance development/recovery workflow; persistent local/shared deployment configuration is otherwise read from `storage.json`;
- `archive push` is the only normal archive command that writes the restic repository; in shared mode it also publishes catalog rows to PostgreSQL after the backup. Phase B exploration/review/Reality commands use the separate object-first/PostgreSQL-last state protocol. Neither path deletes remote objects, and Babel never invokes `restic forget` or `prune`;
- status/verify/inspect/fetch are read-only with respect to the repository;
- `archive verify` is tiered: the default runs restic's structural repository check; `--deep` additionally reads and verifies every repository byte;
- `prepare` emits an immutable preparation/selection ID; `explore --preparation ID` makes corpus scope explicit;
- local prune requires an explicit command and never affects Cellar;
- archive/catalog/retrieval and the Phase A web shell do not require Code or OMP;
- exploration and answer interpretation require a compatible Code capability and never fall back to choosing a model themselves;
- `analysis profile` commands launch Code's configuration-only capability and store only the returned profile ID/revision plus non-secret metadata;
- exploration/review/refinement/Reality commands never publish issues, mutate source repositories, or apply remediation;
- every untrusted dynamic CLI, TUI, log, diagnostic, and preview value passes through one terminal-safe renderer that escapes C0/C1, ESC/CSI/OSC/DCS, bidi, and invisible controls; raw bytes require an explicit private reveal/export;
- machine-readable output goes to stdout and diagnostics to stderr;
- interrupted preparation, exploration, refinement, answer interpretation, and synchronization resume without losing or duplicating committed state;
- the TUI can start, lock, stop, and report the local web listener but never exposes a remote-bind option;
- the web UI uses the same application-service authorization and safe-rendering contracts as CLI/TUI;
- raw Reality answers are durable inputs, while authoritative interpreted facts require explicit plan acceptance; and
- trusted inventory imports may mutate only their configured predicate/entity authority.

### 8.1 Terminal information architecture

The terminal surface is intentionally small: bare `babel` is an instant, offline status overview (build identity, storage configuration state, cached catalog count, web pointer) that never opens the repository. Operational depth lives in the headless subcommands and the web surface. A richer keyboard-driven TUI (jobs, leases, cancellation) is deferred work, reconsidered after Phase B lands its job model; it must never grow a transcript viewer.

### 8.2 Local web information architecture

The React web UI ships in Phase A with **Sessions** (searchable, sortable, filterable OMP/Codex/Claude inventory with metadata detail, artifact/blob closure, completeness reasons, and a paginated best-effort transcript viewer over local files with explicit raw degradation for unknown records) and **Archive** (per-host snapshot coverage, standard and deep verification, snapshot-scoped fetch). The mature Phase B UI adds **Explore**, **Hypotheses**, **Reality**, **Cookbook**, and **Review** areas. Reality contains entity/relationship inspection, alias merge/split history, temporal fact history, conflicts/staleness, trusted-source sync, the prioritized Question inbox, freeform answers, interpreter plans, and atomic accept/reject previews. Review contains hypotheses, findings, proposals, append-only decisions, refinement lineage, and destination previews.

Phase A proves the secure on-demand loopback server (per-launch token, no non-loopback binding) and implements Sessions, session detail with transcript viewing, archive health, verification, and explicit fetch. Privacy masking and an explicit lock/stop control are Phase A follow-ups; Phase B adds every analysis, Reality, question, refinement, and proposal surface. Terminal and web may link to the same durable entity but never maintain independent review state.

## 9. Durable and local state

The first deployed v1 uses **shared mode**: one PostgreSQL database plus one shared restic repository on S3-compatible storage form a single logical Babel backend for every authorized instance. The operator deployment uses Clever Cloud managed PostgreSQL and a Cellar-hosted repository in the same organization/region where practical. The interfaces remain provider-neutral. There is no hosted Babel API or web service; each machine runs the binary and loopback UI locally.

Phase A stores the encrypted archive in the Cellar restic repository and a minimal global catalog/coordination schema in PostgreSQL: deployments, instances, hosts, snapshot IDs/times/order, session identity rows, reconciliation state, idempotency keys, migrations, and server-time fenced leases. Sensitive titles, paths, and transcript metadata stay out of PostgreSQL: they live in the encrypted repository, in live local sources, and in decrypted local SQLite indexes; the Phase A PostgreSQL allowlist contains only schema/version identifiers, opaque IDs/locators, ordering, sizes/counts, commit state, lease/fencing data, and timestamps. Cross-machine browsing of titles and other sensitive metadata therefore arrives with Phase B's encrypted rows, not in Phase A.

An archive publication commits to the repository first: restic writes its snapshot record only after every chunk is durable, and that committed snapshot is archive truth. Babel then idempotently upserts the host's snapshot and session rows into the shared PostgreSQL catalog. If the database step fails, the snapshot remains valid but visibly `catalog-pending`; any authorized instance reconciles snapshot rows from the repository snapshot list, and session rows are reconciled by the owning host's next push or a restore-and-rescan. Loss of the Phase A database is recoverable from the repository plus source rescans.

Phase B extends the existing shared schema with globally durable hypotheses, observations, findings, proposals, Reality entities/facts/questions/answers/plans, review/refinement events, runs, receipts, and evidence references. Large or byte-oriented data remains encrypted in Cellar. Phase B multi-store output commits are object-first and PostgreSQL-last; the PostgreSQL transaction is their visibility boundary.

Configuration and local state use private XDG paths:

- shared/local storage configuration: `$XDG_CONFIG_HOME/babel/storage.json`;
- local state: `$XDG_STATE_HOME/babel/babel.db`, a rebuildable SQLite catalog/cache, decrypted local full-text and Reality query index, and idempotent `catalog-pending`/`pending-sync` journal;
- retained data: `$XDG_DATA_HOME/babel/` for rebuildable fetched sessions and local materializations of encrypted Cellar evidence/exports; and
- cache: `$XDG_CACHE_HOME/babel/` for the restic cache, disposable staging, repository worktrees, sandbox roots, and model-ready redacted inputs.

`babel storage configure --from-json FILE|-` validates a versioned document supplied by a private file or stdin; the managed dotfiles flow uses stdin. It checks deployment/instance identity, repository access, PostgreSQL TLS certificate/hostname, role privileges, schema compatibility, and external key references before atomically replacing the mode-0600 file; it never logs the document. Each instance uses a distinct revocable least-privilege application credential. A separate migration credential is supplied ephemerally to `storage migrate`; normal instances cannot change schema. An unattended archive timer uses the already validated private configuration and never requires Bitwarden to remain unlocked.

Beginning in Phase B, structured identifiers, entity kind/schema version, encrypted-object references, key ID, ciphertext size, commit/sync state, and relationship IDs form the minimal PostgreSQL plaintext allowlist. Titles, claims, operator context, findings, proposals, review notes, receipts, and other sensitive payloads use randomized versioned AEAD envelopes with associated identity/schema data and key IDs before leaving Babel. PostgreSQL never receives plaintext full-text indexes or deterministic ciphertext for search; authorized instances decrypt committed payloads into rebuildable local SQLite indexes.

Immutable entities/events use globally unique client-generated IDs and idempotency constraints. Coordination uses PostgreSQL server time, expiring leases, and monotonically increasing fencing tokens. Each source machine archives only locally available chats under its stable host identity; another instance can browse/fetch committed data but cannot claim that host's publication lease while a valid owner exists. Repository-dependent work records an execution-host constraint.

During a PostgreSQL or Cellar outage, last-synchronized content remains browseable from local cache in read-only mode. Archive objects committed to S3 while PostgreSQL is unavailable remain `catalog-pending`; Phase B outputs remain `pending-sync` and are not globally reviewable or eligible as committed chaos atoms. Reconnection reconciles both idempotently.

Database URLs, encryption keys, and storage credentials remain in Babel's trusted control process and are never exposed to Code, OMP, sandboxes, recipes, browser state, tool arguments, query/bind logs, traces, or diagnostics. Every fully authorized instance can necessarily decrypt the shared corpus; compromise of one has that blast radius. Per-instance credentials, revocation, rotation, and coordinated PostgreSQL/Cellar/key backups are real operational controls.

Invariants:

- local source sessions and fetched session materializations are never modified;
- remote archive/evidence objects are never deleted by normal processing;
- every object, row, and derived result carries a schema version and provenance;
- Phase A archive truth is the restic repository's committed snapshots, and the PostgreSQL catalog is rebuildable;
- Phase B output visibility follows object-first/PostgreSQL-last commits and never claims cross-service atomicity;
- PostgreSQL leases use server time and fencing; stale owners cannot commit;
- one process holds each local state-writer lock; read-only views remain available where safe;
- local SQLite migrations are forward and transactional; PostgreSQL migrations are transactional, serialized, compatibility-checked, and require the separate migration role; and
- logs and errors never contain credentials, DSNs, SQL/bind values, payload ciphertext/plaintext, or raw transcript bodies.

All terminal-facing values—including stdout/stderr, TUI cells, logs, diagnostics, previews, titles, paths, and model text—use the same terminal-safe renderer. Malicious fixtures cover C0/C1 controls, ESC/CSI/OSC/DCS sequences, bidi and invisible controls; only an explicit private reveal/export can emit raw bytes.

## 10. Quality and acceptance requirements

Babel evaluates process quality and usefulness without claiming analytical reliability. A strong developed hypothesis is interesting, inspectable, provenance-bearing, candid about uncertainty, and economical of operator attention. A promoted finding should be specific, connected to supporting and conflicting evidence, and clear about temporal/reality limits; confidence never substitutes for evidence.

Adapter fixtures are generated and synthetic. Each harness has contracts for discovery, catalog metadata, description behavior, raw-log round trip, selective retrieval, malformed inputs including torn lines, and explicit metadata-completeness degradation. Synthetic transcript and credential sentinels cover every captured stdout, stderr, TUI, log, and journal surface; this proves no leakage of known sentinels, not unknown secrets. No real session data enters the public repository or CI.

Archive integration tests start with populated source trees and no Babel state to prove full bootstrap/backfill rather than change-only ingestion. Against the real restic binary and a local-path repository they prove: push captures every adapter backup root, including OMP's blob store; an appended session deduplicates, with added bytes bounded by the change; old and new captures are independently restorable byte-exactly by snapshot ID; the default and `--deep` verify tiers distinguish structural health from full data verification against injected pack corruption; an interrupted backup never yields a partial snapshot, while a backup completing with unreadable files yields a visibly incomplete snapshot and a failing exit; and fetch materializes a session's primary log, artifacts, and resolved blobs byte-exactly and idempotently, while local prune never touches the repository.

Phase A is not complete with fixtures alone. Before real deployment it provisions the Cellar restic repository and Clever Cloud PostgreSQL through the unified shared storage configuration, migrates with the separate role, and completes manual bootstrap from the primary Linux workstation: real pushes covering all three harnesses against Cellar, catalog rows committed and reconciled, all three harnesses visible in TUI/web, one session per harness selectively fetched and byte-verified from a snapshot, and no known transcript or credential sentinel emitted. A second independently configured Babel instance must browse the shared catalog, fetch a session archived by the first host, lose and rebuild its local SQLite cache, and recover cleanly.

The TUI is verified as an actual terminal surface across narrow and wide layouts, keyboard-only navigation, focus visibility, privacy mode, empty/loading/progress/error/offline states, long titles/paths, missing best-effort metadata, malicious terminal-control fixtures, and the real expected session count.

The Phase A web shell is browser-driven against the actual server. Acceptance covers Home/Sessions/fetch/privacy/lock-stop behavior, narrow and wide layouts, keyboard navigation, malicious HTML/Markdown/URL/control fixtures, `Host`/`Origin`/CSRF/DNS-rebinding rejection, one-time session bootstrap, `no-store` responses, CSP/no-remote-assets/no-service-worker enforcement, and proof that no known transcript or credential sentinel reaches URLs, browser history, server logs, or cached responses.

Shared-infrastructure acceptance proves provider-neutral local mode plus the real Clever Cloud shared mode; TLS hostname/certificate rejection; per-instance application-role isolation and revocation; migration-role separation; server-time fenced host leases; idempotent concurrent writers; snapshot-committed/`catalog-pending` database outage recovery; PostgreSQL catalog rebuild from the repository snapshot list plus source rescans; coordinated PostgreSQL/repository/config-key backup documentation, including restic repository password custody; and an hourly user timer on each enabled source host after manual bootstrap.

Managed-provisioning acceptance runs through the actual dotfiles Bitwarden lock/unlock path. It proves stdin-only handoff, atomic configuration replacement, previous-config preservation on invalid credentials or interrupted activation, migration credentials absent from normal instance state, secrets absent from `/nix/store`, argv, environment captures, temporary files, logs, and journal output, vault relock, credential rotation, per-instance revocation, and timer enablement only after shared-storage/bootstrap health passes.

Phase A rollout acceptance records the exact Babel source revision, locked Nix derivation/output path, `babel version --json` result, storage-schema version, dotfiles revision, and activation time. The Linux user units are exactly `babel-archive.service` and `babel-archive.timer`; the timer uses `OnCalendar=hourly` and `Persistent=true`, and the oneshot service executes Babel through its absolute pinned Nix-store path rather than `PATH`. Acceptance proves a missed run executes after login, overlapping activation is fenced, and the first timer run cannot precede bootstrap health.

Phase B uses generated fixtures plus private operator-reviewed corpora spanning all three harnesses. Evaluation records useful novel-candidate yield, reviewer attention cost, unsupported claims, duplicates, evidence diversity, temporal-status mistakes, retrieval/tool value, sandbox containment, and adapter coverage. It defines machine-checkable minimum evidence contracts for observations and findings, model-supplied classifications, and Babel versus human evaluator responsibilities. It does not treat disagreement with a creative hypothesis as a product failure by itself. Code integration tests use a fake executable and structured events; sandbox and broker tests require no real provider or credentials.

Phase B is not complete with fakes alone. It must pass the Code capability/version handshake, exact broker protocol, shared-state security/commit, and challenger/synthesizer gates; run at least one real configured-profile exploration and live containment/escape scenario in every enabled sandbox backend, with analysis disabled on any platform whose backend has not passed; execute a harmless repository experiment; retrieve brokered public evidence; preserve uncategorized and descendant hypotheses; demonstrate a critical challenger changing or preserving a conclusion with unresolved objections intact; demonstrate append-only review across hypotheses/findings/proposals and an atomic reject-and-refine operation with lineage; prove refinement prompts and structured results cover `none`, `alongside`, and `instead` durable-learning assessments without silently authorizing memory; commit and browse the required client-side-encrypted PostgreSQL rows and encrypted Cellar objects from a second Babel instance; show outage-staged output as `pending-sync` and idempotently synchronize it; restore a coordinated PostgreSQL/Cellar/key fixture; export raw run/hypothesis/observation/finding/proposal JSON and Markdown; cancel a live investigation without orphan processes; and verify that sandbox commands cannot read host files, database/provider credentials, encryption keys, agent sockets, or direct network destinations. Dependency-aware pruning applies to local materializations while authoritative remote evidence remains retained; richer retention UX remains Phase C.

Phase B Reality acceptance proves stable entity identity across repository rename/path/chat aliases; reversible merge/split; as-of and current fact queries; trusted-source predicate limits; TTL staleness and conflict handling; raw-answer durability; answer-interpreter retry; one-plan atomic acceptance/rejection; no model-authorized fact mutation; prioritized/deduplicated questions; context snapshots in receipts; and preservation of hypotheses when focus policy defers expensive investigation. It includes one operator-answer flow and one versioned dotfiles inventory import covering project lifecycle plus service-to-host reality.

## 11. Failure behavior

- A source changing during capture yields a crash-consistent file, never a claimed stable one; parsers tolerate torn lines and the next snapshot supersedes it.
- An interrupted backup publishes no partial snapshot; a re-run uploads only chunks the repository does not already hold, and the repository remains valid throughout.
- An unavailable archive leaves the last complete local catalog browsable and marked stale.
- A failed or cancelled fetch leaves no claimed session materialization and preserves prior data.
- Repository damage is detected by `verify` (structural) or `verify --deep` (every byte); never-pruned history plus restic's repair tooling bound the loss, and Babel never masks a failed check.
- Unsupported or changed Codex/Claude formats preserve raw logs and mark metadata/normalization incomplete.
- Missing or incompatible Code disables inference only; archive and deterministic preparation continue.
- A missing saved profile or material policy change pauses scheduled inference until reauthorized.
- Worker or refinement failure records the exact failed run and preserves independent successes and originals; an unavailable durable store leaves newly staged output visibly `pending-sync` rather than globally committed.
- A PostgreSQL outage after a backup leaves it `catalog-pending`; reconciliation from the repository snapshot list restores shared visibility without republishing bytes.
- A Cellar outage prevents new snapshots; PostgreSQL never references a snapshot restic did not report committed.
- A failed vault retrieval, validation, or configuration rotation preserves the previous valid `storage.json`, emits no secret, and does not enable or restart the archive timer with partial state.
- Hosted inference is refused if disclosure preview or redaction cannot complete.
- A hypothesis with missing or invalid provenance remains a visibly degraded candidate and cannot be promoted to a provenance-bearing finding.
- Partial session, repository, experiment, or research coverage is prominent; Babel never presents it as universal analysis.
- No failure path falls back to remote deletion, whole-corpus download, unapproved provider selection, host mutation, direct network access, or issue publication.

## 12. Delivery sequence

### Phase A: prove the archive-backed public product shell

- add the MIT-licensed Go module `github.com/atyrode/babel`, Bubble Tea/cli-kit application, synthetic fixtures, and static release builds matching Code's Linux/Darwin amd64/arm64 platforms;
- adopt restic as the archival engine behind a narrow injected wrapper (init/backup/snapshots/check/dump/restore), pin its version range in dotfiles, and record the never-forget/never-prune retention policy plus the crash-consistent capture model;
- implement staging-free Discover/Describe adapters for OMP, Codex, and Claude Code, including raw logs, best-effort metadata, artifact closure, and per-adapter backup roots;
- capture OMP sibling artifacts and the content-addressed blob store as backup roots;
- provision the Cellar restic repository plus Clever Cloud PostgreSQL through one provider-neutral shared storage document supplied by dotfiles;
- implement the Phase A PostgreSQL schema, migrations, per-instance roles, server-time fenced host leases, idempotent shared catalog, snapshot-to-catalog reconciliation, and catalog rebuild;
- build the rebuildable local SQLite catalog/cache and private session materialization store;
- implement bare-Babel operational Home/Sessions/System, all-harness catalog, privacy mode, metadata detail, explicit selective fetch, selection-scoped local prune, crash/outage recovery, and local-web lifecycle controls;
- implement the embedded TypeScript/React thin web shell with Home, Sessions, shared storage health, metadata detail, fetch, privacy mode, and lock/stop;
- implement headless storage/push/status/verify/list/inspect/fetch/web commands with local development/recovery selection;
- prove local-path repositories first, manually bootstrap the primary Linux workstation, pass the real Cellar/PostgreSQL and two-instance acceptance, then enable the hourly user timer on each approved source machine;
- package Babel as a commit-pinned Nix dependency in dotfiles; and
- keep the old backup job enabled until all three adapters, shared reconciliation, and replacement timers are proven.

The Phase A Linux rollout contract is declarative and reversible. Dotfiles locks the Babel source revision and Nix derivation hash, renders `babel-archive.service` and `babel-archive.timer`, and records the realized executable path in bootstrap evidence. Activation configures and verifies storage first, performs the manual bootstrap/backfill and two-instance checks, then enables the timer. Rollback disables/stops the Babel timer, reactivates the immediately preceding successful dotfiles/Home Manager generation, verifies the legacy backup remains enabled, and never deletes Babel snapshots or rewinds the shared catalog. A replacement version must pass the same manual gate before its timer is re-enabled.

This phase contains no model inference or transcript viewer. Local coding and synthetic fixture work precede the shared-deployment gates (§14); publication to the shared Cellar repository begins only after they pass.

### Phase B: prove open-ended contained exploration

- normalize OMP, Codex, and Claude Code logs into the common event/provenance model, preserving opaque unsupported records;
- build provenance-preserving full-text/structured retrieval, relationship links, the durable hypothesis frontier, and the mandatory hypothesis→observation→finding→proposal path;
- implement stable Reality entities/aliases/relationships, immutable temporal facts, explicit source authority, trusted versioned inventory import, context snapshots, deterministic focus rules, and the prioritized Reality Inbox;
- implement durable freeform question/answer records and the Code→OMP Answer Interpreter, with reviewable multi-action plans and operator acceptance required for authoritative fact/entity/policy changes;
- implement automatic deterministic preparation, sensitive-data preflight, immutable preparation IDs, and machine-checkable evidence minima/classifications with Babel and human evaluator roles;
- pass Code's versioned profile/worker capability handshake and implement its configuration and sandboxed-worker modes;
- implement ephemeral repository snapshots, bounded command/test execution, resource controls, cancellation, and complete tool receipts;
- pass the exact declassification-aware broker protocol gate for public research and pinned public-repository materialization;
- ship open discovery, all eight non-exhaustive lens definitions, five default-enabled baseline lens recipes, three draft lenses, and off-by-default chaos runs with marked/blind atom presentation;
- run broad discovery through a supervised Code→OMP worker, persist every candidate before sorting, require a separate critical challenger before promotion, synthesize exploration and critique without default agreement, and allow investigations and distinct refinement runs to emit immutable descendants;
- extend the existing shared PostgreSQL/Cellar foundation with client-side-encrypted analysis, Reality, question, output, review, refinement, run, receipt, and evidence records through the Phase B object-first/PostgreSQL-last protocol;
- expose globally browseable committed state and visibly pending-sync, idempotently recoverable outage staging; surface scope/capability grants, preparation/refinement/answer-interpretation status, exploration progress, frontier lineage, receipts, hypotheses, observations, findings, review history, Reality context, questions/plans, evidence, remote commitment, and pending-sync state in the TUI;
- implement the rich React web areas for Explore, Hypotheses, Reality, Cookbook, and Review over the shared application services;
- implement private review across hypotheses/findings/proposals, append-only decisions, attributed operator context, Reality Questions/plans, and raw run/hypothesis/observation/finding/proposal JSON and Markdown export;
- run a self-analysis over Babel's own pinned repository, specification, cookbook, and prior analysis artifacts; and
- keep inference explicit by default.

### Phase C: operationalize and integrate

- expand the approved source-host rollout and verify hourly archive, shared-catalog, reconciliation, and credential-revocation health across the managed fleet;
- add richer local retention/prune UX and operational health review;
- add per-instance database-role revocation, payload-key rotation, coordinated PostgreSQL/Cellar/key backup and restore drills, and monitoring;
- expose continuation capture/preflight APIs and integrate Code's Cloud Sessions **Continue here** flow for OMP;
- add sanitized destination projections such as issue-draft export; and
- add opt-in scheduled analysis using a named Code profile with policy guards.

### Phase D: deepen the feedback system

- tune frontier scheduling, clustering, contradiction exploration, optional semantic retrieval, and controlled chaos-atom experiments;
- improve Babel-on-Babel meta recipes and analyze the analysis process longitudinally;
- turn selected review outcomes into curated evaluation material without converting model output into independent evidence;
- compare interaction quality before and after accepted improvements;
- explore authenticated private-remote materialization only with a credential-isolating broker; and
- explore Codex/Claude continuation only if their formats and target launchers support it.

No phase grants Babel permission to publish or apply its proposals.

## 13. Decisions recorded by this audit

1. Babel is a public MIT-licensed Go application at module path `github.com/atyrode/babel`; the embedded TypeScript/React web application is the primary surface and the terminal dependency stays minimal (Bubble Tea and `atyrode/cli-kit` enter only if the deferred TUI is revived).
2. Babel is harness-agnostic across OMP, Codex, and Claude Code for backup, cataloging, selective retrieval, normalization, and analysis.
3. OMP is the highest-fidelity adapter; Codex/Claude preserve raw logs and report best-effort metadata completeness rather than being omitted.
4. Babel uses a clean restic repository under its own prefix and ignores the legacy remote archive; the legacy data is never deleted.
5. Babel exposes one logical storage backend composed of PostgreSQL for shared structure/coordination and a shared restic repository for immutable archive bytes; the external `restic` binary provides encryption/deduplication/transport, and a local-path repository plus SQLite supports development and recovery.
6. Archival is delegated to restic (operator decision 2026-08-27), replacing the audited bespoke `babel/v1` object contract; local development needs no gate, and shared deployment is gated by §14.
7. Source machines snapshot their local source roots hourly under stable host identities; snapshots are per-host, append-only, and never rewritten.
8. Push results report per-adapter backup-root coverage; host display names are catalog rows where the newest value wins.
9. Session identity is host/harness/adapter-defined source identity; historical captures are addressed by restic snapshot ID, and bare selectors choose the latest snapshot.
10. Dotfiles commit-pins Babel through Nix, provides stable host/instance identity, retrieves secrets, pipes one storage document into Babel, and enables an hourly user timer only after manual bootstrap acceptance.
11. V1 never deletes remote data — Babel never runs `restic forget` or `prune`. Explicitly fetched session materializations persist locally until explicit prune.
12. The web interface is the primary interactive surface and bare `babel` is a minimal offline status overview (operator decision 2026-08-28, revising the earlier TUI-first record); Phase A catalogs all three harnesses and remains metadata-only despite its shared infrastructure.
13. The first deployed Phase A uses Clever Cloud PostgreSQL plus a Cellar restic repository, proves two-instance catalog/fetch/rebuild behavior, and requires a gated real three-harness round trip.
14. Babel is an exploratory instrument, not a reliable automated auditor; it guarantees containment, provenance, reproducibility metadata, and no mutating/publishing external effects rather than correctness of ideas.
15. Discovery has an open hypothesis space. Every candidate is preserved before post-hoc sorting, and finite runs checkpoint a durable recursive frontier.
16. Observations are provenance-bearing claims over session, repository, experiment, or research evidence; candidates develop only through hypothesis→observation→finding→proposal.
17. AI exploration of normalized material from every source harness requires compatible Code→OMP; Babel has no direct-OMP fallback, provider clients, or generic analyzer plugin system in v1.
18. Code owns saved analysis profiles and provider/model/thinking policy; Babel owns the ephemeral sandbox, capability grants, evidence brokers, hypothesis lineage, receipts, and review.
19. The sandbox may mutate disposable clones and run tests but cannot access credentials, mutate source repositories, push, publish, or use direct network access.
20. Phase B includes brokered unauthenticated public research and pinned public repositories; private remote credentials never enter the worker.
21. Babel ships shared investigation policies and non-exhaustive lenses, supports arbitrary uncategorized hypotheses, and can analyze Babel and prior analyses themselves.
22. One Code profile applies to a run. Preparation is automatic; inference is explicit unless an operator enables a guarded schedule.
23. Cross-machine **Continue here** creates a new local OMP fork from an immutable verified snapshot; exact machine/workspace restoration is not promised.
24. Analysis/review/refinement never publishes issues, mutates source repositories, or applies remediation.
25. Chaos is an explicit off-by-default run type linked to a clean control. Any durable entity revision can be an atom with exact lineage, but it is stimulus only; recursive model/ancestor/descendant reuse never becomes corroboration.
26. The canonical output is a private proposal with suggested destinations; Phase B exports raw private artifacts, while Phase C adds sanitized projections such as self-contained GitHub issue drafts.
27. Review and refinement are durable, browseable, append-only, and lineage-preserving: reject never deletes, operator context is guidance rather than evidence, and reject-and-refine atomically records rejection plus an authorized independent descendant run.
28. Shared infrastructure begins in Phase A: the restic repository's committed snapshots are recoverable archive truth and PostgreSQL is the rebuildable global catalog/coordination plane. Phase B extends PostgreSQL plus encrypted Cellar into the visibility boundary for globally browseable analysis, Reality, question, review, refinement, and lineage state.
29. A hypothesis can be promoted to a finding only after a deliberately skeptical, logically separate challenger pass; the final synthesizer evaluates both exploration and criticism without defaulting to agreement and preserves unresolved objections.
30. Babel models non-GitHub context through stable entities and an append-only temporal Reality Ledger, not freeform global memory; only operator actions and predicate-scoped trusted imports authorize facts.
31. Discovery persists ideas before context affects focus. Immutable context snapshots and explicit rules may defer expensive investigation or targeting without deleting hypotheses; lifecycle and ownership never silently imply policy.
32. Reality Questions are first-class outputs. Every freeform answer is retained and interpreted by a versioned Code→OMP agent into a reviewable multi-action plan; authoritative fact/entity/policy changes require operator acceptance and issue publication remains impossible.
33. The TUI is the operational surface and the TypeScript/React local web UI is the rich exploration/review surface. Phase A ships a thin on-demand loopback-only web shell; Phase B adds Reality and analysis workflows.
34. The web server has no v1 remote/LAN/persistent mode and enforces one-time session bootstrap, host/origin/CSRF checks, no-store, restrictive CSP, no external assets/service worker, universal untrusted rendering, and explicit lock/stop.
35. The first deployment is multi-machine shared mode, not a hosted Babel service: each authorized machine runs its own binary/TUI/loopback web UI, archives only local chats, and connects directly to the common storage backend.
36. The operator deployment uses Clever Cloud Cellar and managed PostgreSQL; provider compatibility remains S3 plus PostgreSQL rather than Clever Cloud-specific APIs.
37. Development and rollout are staged: local fixtures first, manual primary-Linux bootstrap second, two-instance shared acceptance third, then hourly timers on approved source machines; Phase B capabilities build on the Phase A shared foundation.
38. Managed fleet provisioning is dotfiles-specific: an explicit Bitwarden unlock/retrieve/relock flow pipes common storage/key material plus per-instance credentials to Babel over stdin, atomically configures it, and gates the hourly timer; Babel remains vault-agnostic.
39. Every refinement agent must assess whether reviewer feedback is output-specific, should produce a separate durable-learning proposal alongside a revision, or should produce durable context instead of a replacement output; no proposed memory becomes authoritative without destination-appropriate operator review.
40. Archival is restic's: encryption, content-defined deduplication, snapshot format, and integrity checking. Babel wraps init/backup/snapshots/check/dump/restore behind an injected port and never invokes `forget` or `prune`.
41. Captures are crash-consistent per file, not transactional across files; parsers tolerate torn lines, the next hourly snapshot supersedes, and continuation-grade claims require adapter-verified closure at read time.
42. `archive verify` is tiered: the default runs restic's structural check; `--deep` reads and verifies every repository byte.
43. The session catalog is built from live local sources by adapters and shared via PostgreSQL; it is rebuildable convenience state, never archive truth.
44. Adapters declare backup roots separately from discovery roots; OMP's backup roots include the content-addressed blob store so every snapshot holds a continuation-grade closure superset.

## 14. Deferred decision gates

The following gates apply before the named deployment or capability ships; they do not block earlier local fixture-driven coding.

### Before the first shared Phase A deployment

- Freeze the unified `storage.json` schema, deployment/instance/host IDs, provider-neutral local/shared modes, restic repository locator and PostgreSQL TLS/role fields, redaction, external-secret references, compatibility rules, and atomic stdin-only configuration replacement.
- Freeze and migrate the minimal Phase A PostgreSQL schema, plaintext allowlist, snapshot/session-to-catalog mapping, idempotency, server-time fenced host leases, `catalog-pending` state, reconciliation, and complete catalog rebuild from the repository snapshot list plus source rescans.
- Prove local-path-repository/SQLite development mode, real Clever Cloud Cellar/PostgreSQL mode, migration/application role separation, per-instance credential revocation, TLS failure behavior, concurrent writers, database/repository outages, two-instance cache rebuild/browse/fetch, and no PostgreSQL dependency in direct archive recovery: the restic binary plus the repository password alone restore every source tree.
- Document and exercise coordinated PostgreSQL catalog backup, repository recovery, restic repository password custody and backup, storage configuration recovery, manual primary-host bootstrap, hourly timer enablement, and rollback to the still-enabled legacy backup.
- Prove the dotfiles-specific Bitwarden unlock/retrieve/relock handoff, common versus per-instance secret split, Nix-store/argv/environment/temp/log secrecy, invalid-rotation rollback, migration-credential ephemerality, per-instance revocation, and timer gating; track that implementation in `atyrode/dotfiles` without adding Bitwarden knowledge to Babel.

### Before Phase B exploration is accepted

- Select and threat-model the Linux and Darwin sandbox backends, including filesystem isolation, process/resource controls, teardown, and escape assumptions.
- Define and enforce the exact evidence-tool and public-research broker protocols: disclosure-sink handling for URL/query/header/body/redirects, validated templates or opaque IDs, declassification/consent, SSRF/redirect/content limits, provenance fields, pinned public-repository materialization, and isolation of Code/OMP provider transport and credentials from tool processes.
- Define frontier identity, recursive and refinement lineage, checkpoint/resume, novelty, clustering, generated-evidence rules, and append-only review semantics.
- Define machine-checkable observation/finding evidence minima, model-supplied classifications, and Babel versus human evaluator responsibilities.
- Define chunking and context strategy from measured OMP/Codex/Claude corpus sizes without making retrieval limits constrain hypothesis categories.
- Decide how much verbatim evidence normal views show versus reveal through private locators.
- Define the operator-approved scope/capability UX and how current local repositories are snapshotted without exposing ambient machine state.
- Define evaluation and review sampling for useful emergence, operator attention, unsafe behavior, provenance loss, and retrieval diversity.
- Decide whether one profile per run is sufficient or measured workloads justify Code-owned profile mappings.
- Define clean-control matching, chaos atom schemas/sources, immutable revision/lineage recording, reusable packs, marked/blind presentation, synthetic-atom lineage, randomization, quarantine, clean-reinvestigation, and reviewer-blinding rules before enabling chaos outside development.
- Extend the Phase A PostgreSQL/Cellar protocol for Phase B object-first/database-last commits, required object/row closure, global entity IDs/idempotency, host-pinned analysis work, second-instance review/continuation, and `pending-sync` behavior.
- Freeze client-side randomized AEAD payload envelopes, key IDs/rotation compatibility, sensitive payload schemas, local-only decrypted search indexing, and the compromised-authorized-instance blast-radius statement.
- Prove a coordinated Phase B PostgreSQL ciphertext/Cellar object/external-key backup and restore fixture; recurring rotation/restore drills and monitoring ship in Phase C.
- Define challenger and synthesizer input/output schemas, their logical separation from the explorer and each other, evidence-authority limits, objection-preservation representation, promotion eligibility and ownership, and evaluation of whether criticism improves conclusions without rewarding performative negativity. The synthesizer may consolidate only locator-backed observations and recorded challenger objections/counter-evidence; unsupported additions remain hypotheses, and only Babel's control plane may apply the promotion transition after validating the structured result.
- Freeze Reality entity/alias/relationship schemas, reversible merge/split, fact revision/authority/freshness/conflict semantics, trusted-source predicate scopes, context snapshots, focus-rule evaluation, question/answer/plan states, Answer Interpreter schema, retry/idempotency, and atomic operator acceptance before Reality context may control Phase B expenditure.
- Define the React application/API contract and prove that Reality/review mutations share the Go service authorization path rather than becoming browser-owned state.

### Before scheduled inference

- Define the exact material-change fingerprint.
- Define allowed disclosure classes, cost guards, retry ceilings, and schedule cadence.
- Define how profile reauthorization is recorded and how a paused schedule is surfaced.

### Before OMP Cloud Sessions continuation

- Define the minimum repository fingerprint that distinguishes a compatible checkout from a misleading same-path checkout.
- Define stable/inactive snapshot criteria under periodic host-scoped publication.
- Verify complete OMP artifact/blob closure and the local fork/import path against current OMP.

### Later product questions

- Decide whether selected review outcomes become evaluation material automatically or only through explicit curation.
- Evaluate semantic retrieval as a diversity mechanism rather than assuming vector similarity is necessary.
- Explore authenticated private-remote materialization only after a broker can prove credential non-exposure and no push authority.
- Explore Codex/Claude continuation only after archive and analysis adapters expose reliable semantics.
