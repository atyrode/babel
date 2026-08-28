# Changelog

All notable changes to Babel are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/) with a `0.x` pre-stability series.

Entries up to v0.1.0 reference commit hashes; development is PR-based from
2026-08-28 onward, so later entries reference pull-request numbers.

## [Unreleased]

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

[Unreleased]: https://github.com/atyrode/babel/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/atyrode/babel/releases/tag/v0.2.0
[0.1.0]: https://github.com/atyrode/babel/releases/tag/v0.1.0
