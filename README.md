# Babel

Babel is an open-ended exploratory instrument for archived conversations from OMP, Codex, and Claude Code. It helps unexpected ideas emerge about the operator's systems, code, tools, processes, interactions, and Babel itself.

It owns encrypted session archival, retrieval, normalization, a versioned analysis cookbook, provenance-bearing hypothesis exploration, and human review. Analytical output is creative, fallible, and incomplete—not an automated audit or source of truth. Babel makes ideas and their evidence inspectable; it does not open issues, edit repositories, rotate credentials, or apply suggested improvements.

The audited product and delivery specification is in [SPEC.md](SPEC.md), and the exercised recovery, custody, and rollback procedures are in [docs/runbook.md](docs/runbook.md). Babel is a public Go application whose primary surface is a TypeScript/React web application compiled into the static Go binary and served loopback-only by `babel web`; the terminal surface stays minimal.

Running `babel` prints a fast offline status overview; `babel web` starts the on-demand loopback-only web interface, Babel's primary interactive surface. Development begins locally, but the first deployed v1 is multi-machine: every authorized Babel instance connects to one logical shared backend composed of PostgreSQL for catalog/coordination/structured state and a shared restic repository on S3-compatible storage for the immutable encrypted archive. The operator deployment uses Clever Cloud PostgreSQL plus a Cellar-hosted restic repository; provider-neutral local-path-repository/SQLite modes remain available for fixtures and recovery. Phase A proves all three harnesses against real storage and exposes the shared metadata catalog through the web and headless surfaces. Open-ended Code→OMP exploration follows in Phase B.

On managed machines, `atyrode/dotfiles` performs an explicit Bitwarden unlock/retrieve/relock bootstrap, combines shared Cellar/PostgreSQL/key material with machine-specific identity and application credentials, and pipes the versioned document to `babel storage configure --from-json -`. Babel never invokes Bitwarden, and secrets never enter Nix derivations, argv, shell history, logs, or persistent temporary files.

Hypotheses and findings are retained exploratory state; private proposals and Reality Questions are first-class review outputs. Questions collect or refresh non-GitHub context through attributed operator answers and agent-interpreted, human-approved Reality Ledger updates. For bounded repository work, the primary projection is a sanitized, self-contained GitHub issue draft; cross-system, operator, skill/runbook, investigation, effective-pattern, cookbook, and sensitive-security outputs use purpose-fit private documents. Babel never publishes them.

## Core loop

1. Archive agent sessions and retrieve encrypted snapshots through Babel's archive subsystem, deployed and scheduled by `atyrode/dotfiles`.
2. Discover, normalize, hash, and index sessions without modifying their source.
3. Explore a selected or broad scope inside a contained Code→OMP sandbox.
4. Preserve every emergent candidate in a resumable hypothesis frontier.
5. Search other discussions, repositories, history, experiments, and public research to develop or challenge ideas.
6. Ask prioritized Reality Questions when ownership, lifecycle, fleet, deployment, identity, or focus context is missing, stale, or disputed; preserve raw answers and interpret them into reviewable action plans.
7. Browse hypotheses, findings, proposals, questions, append-only review decisions, and Reality Ledger revisions persistently; rejected ideas and superseded facts remain retained.
8. Feed useful human-approved ideas into repositories, practices, trusted context, or Babel itself outside the analysis sandbox.

## Design principles

- **Ideas may be arbitrary; actions may not.** Candidates are cheap and permissive. Evidence, containment, and human review become stricter as an idea approaches action.
- **Local and private by default.** Raw transcripts can contain source code, credentials, personal data, and adversarial instructions.
- **Public code, private data.** Babel is public and independently packageable; credentials and rebuildable indexes stay local, while archives and findings are client-side encrypted at rest. An explicitly approved hosted profile receives its selected redacted input in readable form to that provider; transport encryption does not hide it from the provider.
- **Harness-agnostic sources, explicit execution worker.** OMP, Codex, and Claude Code feed one provenance model. Code→OMP investigates every source; only **Continue here** is restricted to OMP sessions.
- **One logical backend, two storage primitives.** Shared PostgreSQL holds global catalog, coordination, relationships, and structured state; a shared restic repository holds the immutable encrypted archive (Phase B evidence and large outputs are separate encrypted S3 objects). Every authorized machine uses the same backend, while local SQLite/files remain rebuildable cache, search index, scratch, and pending-sync state.
- **Archive recovery does not depend on PostgreSQL — or on Babel.** The restic binary plus the repository password restore every archived source tree; the Phase A shared catalog is rebuildable from the repository snapshot list plus source rescans. PostgreSQL becomes the visibility boundary for Phase B analysis/Reality/review commits.
- **One model-policy owner.** Babel owns exploration, containment, and evidence; Code owns provider/model/thinking configuration and the credential-isolated OMP controller.
- **Open hypothesis space, closed action space.** Sandbox and capability boundaries constrain what analysis can affect, not what it may notice or imagine.
- **Critical pressure without a critical monoculture.** A deliberately skeptical challenger attacks assumptions and searches for counter-evidence; a separate synthesizer judges both exploration and critique rather than being instructed to agree with either.
- **Clean controls before chaos.** Chaos is explicit and off by default, linked to a clean control; its non-evidence atoms cannot promote ideas without clean reinvestigation.
- **Provenance over certainty.** Babel reliably records where an idea came from and how it was investigated; it does not promise that the idea is correct.
- **Resumable rather than exhaustive.** Finite runs checkpoint unexplored hypotheses so later inference can continue without constraining initial emergence.
- **Reality is versioned, not remembered.** Stable entities and append-only, temporal, provenance-bearing facts ground attention without becoming an opaque global prompt; agent-inferred context remains proposed until a human or trusted source authorizes it.
- **Context controls attention after emergence.** Hypotheses are preserved before current lifecycle, ownership, fleet, and focus policy guide expensive investigation or output targeting.
- **Suggestions, never side effects.** Integrations may render issue drafts, but publishing or applying them is out of scope.
- **The cookbook is a product.** Analysis recipes are versioned, reviewable assets that improve as useful and harmful patterns are learned.
- **Good patterns matter too.** Babel should preserve effective habits, not merely collect failures.
