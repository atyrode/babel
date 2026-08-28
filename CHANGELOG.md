# Changelog

All notable changes to Babel are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/) with a `0.x` pre-stability series.

Entries up to v0.1.0 reference commit hashes; development is PR-based from
2026-08-28 onward, so later entries reference pull-request numbers.

## [Unreleased]

### Added

- A `flake.nix` dev shell pinning the full toolchain (Go, restic, Bun,
  PostgreSQL client, `CGO_ENABLED=0` matching the release builds), so
  `nix develop` replaces ad-hoc `PATH` exports to nix store paths ([#24]).
- The Phase A shared catalog schema and migrations: deployments, instances,
  hosts, snapshots, opaque session identity, server-time fenced host leases,
  and idempotency keys, applied transactionally from embedded SQL ([#25]).
- **The SPEC.md §9 plaintext allowlist is now enforced, not documented.**
  Every shared-catalog column is enumerated with its permitted data class, and
  `Verify` reflects the live schema and fails on any column or table outside
  it. `Migrate` runs it before reporting success, so a migration that would
  widen the plaintext boundary fails at apply time. Sessions are keyed by an
  opaque digest rather than their selector (which embeds a workspace-derived
  project slug), hosts carry no display name, and no session quality verdict
  is stored ([#25]).
- Migration and application role separation for the shared catalog: an
  application role receives DML on catalog tables, read-only access to the
  migration ledger, and no DDL, so a normal instance cannot change schema or
  claim a migration it did not apply. Credentials are per-instance and
  revocable without disturbing other instances. DDL identifiers and passwords
  are quoted by PostgreSQL's own `format()` rather than string concatenation,
  the rendered statement is never included in an error, and the supplied
  password is redacted from any error it does produce ([#26]).
- The migration ledger is covered by the same enforcement as the rest of the
  schema: it is created by the runner (so it can be read before deciding what
  to apply), its live shape is asserted against PostgreSQL's own catalog rather
  than the migration text, its recorded version persists across connections so
  a restarted instance reapplies nothing, and dropping it is a discrepancy
  `Verify` reports.

[#24]: https://github.com/atyrode/babel/pull/24
[#25]: https://github.com/atyrode/babel/pull/25
[#26]: https://github.com/atyrode/babel/pull/26

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

[Unreleased]: https://github.com/atyrode/babel/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/atyrode/babel/releases/tag/v0.2.1
[0.2.0]: https://github.com/atyrode/babel/releases/tag/v0.2.0
[0.1.0]: https://github.com/atyrode/babel/releases/tag/v0.1.0
