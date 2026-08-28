# Changelog

All notable changes to Babel are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/) with a `0.x` pre-stability series.

Entries reference the commits (and, once development moves to pull requests,
the PR numbers) that landed them.

## [Unreleased]

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

[Unreleased]: https://github.com/atyrode/babel/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/atyrode/babel/releases/tag/v0.1.0
