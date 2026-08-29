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
- Migration and application role separation for the shared catalog, usable only
  where a provider permits creating database users: an application role
  receives DML on catalog tables, read-only access to the migration ledger, and
  no DDL, so a normal instance cannot change schema or claim a migration it did
  not apply, and credentials are per-instance and revocable without disturbing
  other instances. DDL identifiers and passwords are quoted by PostgreSQL's own
  `format()` rather than string concatenation, the rendered statement is never
  included in an error, and the supplied password is redacted from any error it
  does produce ([#26]). **Clever Cloud's managed PostgreSQL cannot create
  database users** (provider confirmation, 2026-08-28), so this is not the
  operator deployment's arrangement and nothing outside its own tests calls it;
  see the SPEC amendment under Changed.
- The migration ledger is covered by the same enforcement as the rest of the
  schema: it is created by the runner (so it can be read before deciding what
  to apply), its live shape is asserted against PostgreSQL's own catalog rather
  than the migration text, its recorded version persists across connections so
  a restarted instance reapplies nothing, and dropping it is a discrepancy
  `Verify` reports.
- Server-time fenced host leases and exactly-once publication: acquire, renew,
  and release with a monotonic per-host fence, and `PublishSnapshot`, which
  records a snapshot and its session rows under a lease it validates with a row
  lock both before writing and immediately before commit. A writer with a
  superseded fence, or whose lease expires mid-publication, lands nothing. A
  repeated idempotency key is a no-op, so a retried push after a lost response
  is safe. Session identity is an opaque digest over deployment, host, harness,
  and source id.
- Lease expiry is judged against `clock_timestamp()` rather than `now()`.
  PostgreSQL's `now()` is the transaction timestamp and is frozen for the whole
  transaction, so a lease validated inside a long publication could never
  observe an expiry that happened while that transaction ran, and the TTL
  bounded nothing.
- Reconciliation and catalog rebuild: the repository snapshot list is truth, so
  `Reconcile` adopts snapshots the catalog lacks as `catalog-pending`, never
  downgrades what a push already committed, and reports snapshots the
  repository no longer lists as an anomaly rather than deleting them (retention
  is append-only). `Rebuild` reconstructs a host from the listing alone, which
  is the recovery path for a lost Phase A database, and is deterministic so two
  instances recovering independently agree on ordering.
- Snapshot attribution is checked rather than assumed: a listing that mixes
  hosts, names a different host, or contains a snapshot recorded without
  `--host` is refused before anything is written, and host ids are validated
  with the same rule `--host`, `BABEL_HOST_ID`, and `storage.json` enforce.
  Session rows cannot come from the snapshot list, so a rebuilt host has none
  until its owner pushes again.
- **Correction.** An earlier entry in this release claimed restic reports
  backup counts only on its backup message and not in `snapshots --json`. That
  was wrong: restic stores a summary in the snapshot record, so `files_new`,
  `files_changed`, `files_unmodified`, and `data_added` are available from the
  listing. The claim came from reading Babel's own wrapper struct, which did
  not parse the field, instead of restic's actual output. `Snapshots` now reads
  it, and reconciliation and rebuild record real counts instead of discarding
  recoverable truth.
- Unknown counts are stored as SQL NULL rather than zero. A snapshot whose
  restic record carries no summary has counts that are unknown, and writing
  zero would assert it backed up nothing; the owning host's next push replaces
  NULL with real values. Session count is nullable for the same reason -
  reconciliation cannot know it without reading the snapshot's file tree.
- `restic ls` is wrapped, so a snapshot's file tree can be enumerated from
  metadata alone without downloading contents - the primitive cross-host fetch
  needs.
- Archived-session identification: each source adapter can recognize its own
  sessions in a snapshot's file listing, assigning the same source identity a
  local scan would. Identification is a pure function of the listing - no
  filesystem, no downloads - which is what lets one machine enumerate another
  machine's archived sessions. Each adapter is proved equivalent to its own
  `Discover` over the shared fixtures, and a combined listing holding all three
  harnesses' trees is proved to partition cleanly, so no adapter can claim
  another's files.
- Blob and attachment attribution is deliberately not inferred from paths.
  Which content-addressed blobs a session references lives inside its primary
  log, and identification does not read logs, so a closure derived from the
  listing covers the primary log and path-attributable sibling artifacts only.
  Guessing would fabricate a closure a fetch could not honour.
- `sessions fetch --host ID` materializes a session archived by any host,
  resolving the selector inside that host's snapshot instead of against local
  sources. It addresses a session this machine never had, or one whose local
  files are gone, and restores it byte-exactly. Identification reads only the
  snapshot's file listing, so no transcript bytes are downloaded to find a
  session. Selecting a host with no snapshots names the hosts that do have
  them rather than falling back to another machine's.
- A leak-channel acceptance for the web shell (SPEC.md §548): a unique
  sentinel is planted inside transcript content and a second one as the
  repository credential, then every API route, both error paths, the 401, and
  the browser's first load are exercised and searched. The credential reaches
  no response body, header, or log line; transcript content is confined to the
  transcript endpoint and *required* to appear there, so the confinement check
  cannot pass vacuously; every `/api` response is `no-store`; the launch token
  reaches no log line; and no selector carries either sentinel. Each search
  was mutation-tested against a planted leak. Scope limit: it drives the
  server's HTTP surface, not a browser. The client is a hash router, so
  selectors are never transmitted in a URL at all, but they do enter the
  history entry, which is why sentinel-free selectors are what the history
  channel rests on — established there by reading the route table, and now
  enforced by the browser acceptance below ([#34]).
- A browser-driven leak acceptance for the channels Go cannot observe
  (SPEC.md §548). A real headless Chrome drives the served bundle against a
  synthetic corpus carrying a sentinel in a transcript and a sentinel as the
  repository password, and proves: the fragment token authenticates; a reload
  and a back/forward navigation stay authenticated; the transcript actually
  renders, so the page is not passing vacuously empty; no history entry or
  request URL holds either sentinel or the token; no `/api` response is served
  from the browser cache; and a context without the token is refused. A new
  `browser` CI job runs it, and the test hard-fails rather than skipping when
  CI has no Chrome, because a silent skip would retire the gate. The
  address-bar assertion is **non-discriminating**, and the reason was measured
  rather than guessed: two independent mechanisms keep the token out of
  history, the bootstrap's `replaceState` and App.tsx's catch-all
  `<Navigate to="/sessions" replace />`, which the unmatched `#token=…`
  fragment falls through to. Disabling either alone still passes; disabling
  both fails the history walk, which is bounded by the stack the browser
  reports and proves completion by landing on the context's initial blank
  entry, so a traversal that stops early — as it does when a retained token
  entry redirects away on arrival — is a failure rather than a silently checked
  prefix. Failure output reports the traversal with the token redacted. Both
  mechanisms are kept, since either alone is a single point of failure for a
  credential, and the route now says so where an editor would remove it
  ([#36]).

### Changed

- **The web launch token moved from the query string to the URL fragment**, as
  SPEC.md §146 always specified. The token now appears in exactly one place —
  the launch URL's fragment — and the bootstrap erases it from the address bar
  and the history entry on first load. Fragments are never transmitted, so it
  reaches no request line, access log, cache key, or `Referer`. A token
  supplied in a query string is refused rather than honoured, because
  accepting it would reopen every one of those channels. `Referrer-Policy:
  no-referrer` is now set on every response; CSP restricts load destinations
  and never governed the referrer. The nonce-to-cookie exchange §146 also
  describes remains unbuilt, and the bootstrap now documents its one implicit
  coupling: it must read the fragment before the hash router mounts, which ES
  module evaluation order guarantees. Verified in a real browser against a
  synthetic corpus: first load, reload, deep link, and back/forward all
  authenticate with the token absent from every transmitted URL and from the
  address bar after bootstrap, and a context without the token is refused
  ([#35]).
- **SPEC amended: shared mode's supported default is one database credential,
  not a role-separated pair.** A Clever Cloud employee confirmed their managed
  PostgreSQL cannot create database users, so the arrangement SPEC promised in
  eight places — a per-instance least-privilege application role plus a
  separate migration credential — is unavailable on the deployment provider.
  What the default gives up is recorded rather than softened: schema change is
  restrained by operator procedure instead of by privilege, and no
  database-level control can revoke a single instance, leaving fleet-wide
  rotation and repository-password custody as the honest remaining controls.
  Closing that gap with application-level instance revocation is now a Phase A
  requirement and a pre-deployment gate rather than something the code claims
  today. Role separation stays specified and implemented for providers that
  permit it; granting least privilege to a provider-created user is explicitly
  untested on Clever Cloud and may not be relied on until proven against the
  real add-on (decision 46).

### Fixed

- A web-harness test waited exactly as long for graceful shutdown as the server
  gives itself (5s), so a correct-but-slow shutdown and the test's deadline
  raced; under full-suite load the test reported a hang that had not happened.
  The bound now exceeds the server's own budget.
- `--host` was bound by every repository-taking command, so
  `sessions fetch --host ID` was accepted and silently did nothing. It now
  selects the cross-host path; previously the flag parsed and was ignored.
- **Two browser leak assertions raced the page they were asserting about.**
  Back/forward and the unauthorized negative control waited on `location.hash`
  or a character count, both of which settle before the view behind them
  renders, so the content assertion could read `Loading session…` and fail
  roughly one run in six. Each step now waits for its destination's own
  content — or for an authorization failure, so a genuinely broken credential
  still fails fast and by name instead of timing out. Diagnosed from the
  captured failure rather than guessed, and clean across 22 subsequent runs
  ([#38]).

[#24]: https://github.com/atyrode/babel/pull/24
[#25]: https://github.com/atyrode/babel/pull/25
[#26]: https://github.com/atyrode/babel/pull/26
[#34]: https://github.com/atyrode/babel/pull/34
[#35]: https://github.com/atyrode/babel/pull/35
[#36]: https://github.com/atyrode/babel/pull/36
[#38]: https://github.com/atyrode/babel/pull/38

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
