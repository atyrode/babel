# Changelog

All notable changes to Babel are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/) with a `0.x` pre-stability series.

Entries up to v0.1.0 reference commit hashes; development is PR-based from
2026-08-28 onward, so later entries reference pull-request numbers.

## [Unreleased]

### Added

- **The Phase B analysis core**, still on synthetic data only. `internal/index`
  is the provenance-preserving retrieval §5.4 requires and no more: SQLite FTS5
  over source records with structured, temporal, and repository-path filters,
  and deliberately no score, rank, or relevance field, because §5.4's rule is
  that retrieval order never becomes evidence strength. `internal/frontier` is
  the durable hypothesis frontier, where the invariants are structural rather
  than validated — a hypothesis has no status column, only append-only status
  events, so no code path can overwrite a lifecycle; there is no delete
  statement anywhere in the package; an observation row carries a
  `CHECK(evidence_count > 0)` so §4.3's evidence rule survives payload
  encryption, since a store that cannot read sealed evidence can still refuse a
  row claiming none; and a refinement request cannot exist without the
  rejection that authorized it. `internal/cookbook` loads versioned recipes
  through a hand-rolled strict front-matter grammar, ships the five
  default-enabled lenses and three drafts §5.5 names as real analytical
  guidance, and enforces §5.1's version rule with a drift check whose digest
  excludes the version field — otherwise an increment would move the digest and
  the check would have no signal. `internal/run` holds immutable preparation
  records with domain-separated derived IDs and the §7 run receipt, split into a
  plaintext-eligible header and a sensitive body so a deployment that seals
  bodies can still list, order, and chain receipts without a key.
  `internal/preflight` is the deterministic secret and health preflight, whose
  findings carry locators and placeholders but never a secret value.
- **Containment is declared rather than assumed.** A worker's resolved
  configuration must now name its sandbox backend, its filesystem, network,
  resource, and teardown properties, and its own escape assumption, and Babel
  refuses a declaration short of the run's requirement before any job material
  reaches the worker. The strict requirement is the default so an unset field
  fails closed, and even a relaxed run must still name a backend, because a
  receipt that names no boundary cannot tell a reviewer what the evidence was
  produced behind.
- **The four Phase B foundations**, each buildable and testable with no real
  data, no credentials, and no network. `internal/event` is the SPEC §4.1/§6.3
  analysis event model: a streaming per-harness classifier into user reports,
  agent claims, tool observations, repository changes, and verification
  evidence, where an unrecognized or damaged record degrades to an opaque
  partial event and is never dropped. Its 16 MiB record budget is a measured
  constant, not a guess: real harness logs carry single records into the tens
  of megabytes, where a `bufio.Scanner` default would degrade about one record
  in a hundred. `internal/synth` generates a deterministic synthetic corpus
  whose extremes exceed production — a primary log past 320 MiB and a record at
  the budget ceiling — because a fixture smaller than production is a fixture
  that lets production break the reader. `internal/envelope` is the client-side
  AES-256-GCM payload envelope whose additional data binds a ciphertext to its
  row, type, and field, so an envelope moved between rows fails to open, with a
  keyring that seals under one key and opens under every known one.
  `internal/worker` defines the Code analysis-worker protocol, implements
  Babel's whole side of it — version negotiation, per-request authorization
  against the run's grant, process-tree lifetime, output validation, receipts —
  and ships the `Conformance` suite that a real worker must pass, since Code
  does not implement the counterpart yet.
- **A `packages.default` flake output**, so a scheduler in another flake can run
  a pinned Babel from an absolute store path rather than whatever `PATH` holds.
  `version.go` accepts a link-time revision because a Nix build compiles from a
  source copy with no `.git`, where `-buildvcs` stamps nothing; with nothing
  injected the reported identity is byte-identical to before.
- **`archive push --json` reports `snapshot_id` and `incomplete`** as the fields a
  scheduler needs to tell three outcomes apart: a push that archived nothing
  because the host has no source roots, one that archived only part of a tree, and
  a complete snapshot. Exit status alone conflates the first with the third.

- **The two §14 pre-deployment schemas are frozen, and the freeze is
  enforced by tests rather than asserted in prose.** `storage.json` at
  `config_schema` 2 and the catalog at `SchemaVersion` 1 with migrations
  `0001_init` and `0002_unknown_counts` — both having run against real Cellar
  and the real managed PostgreSQL, which is the strongest evidence they were
  going to get before carrying data.

  `internal/config` pins the exact JSON names at every level, the schema
  number, and that a pre-freeze schema-1 document still loads. That last one
  matters because loading *deliberately* ignores unknown names so a newer
  writer's document stays readable — which is precisely why no other test could
  catch an accidental field. Adding a field now fails with both sets printed.

  `internal/sharedcatalog` pins the migration ledger's identity and order, the
  schema version, and the allowlist's table set with a non-vacuity check. The
  schema changes by adding a migration, never by editing one that has run: an
  applied migration is history, and rewriting it leaves every deployment that
  ran it holding a shape nothing describes.

  Frozen does not mean unchangeable. It means a change is a deliberate act
  with a compatibility story, and the way to have that conversation is for
  these tests to fail.

- A `flake.nix` dev shell pinning the full toolchain (Go, restic, Bun,
  PostgreSQL client, `CGO_ENABLED=0` matching the release builds), so
  `nix develop` replaces ad-hoc `PATH` exports to nix store paths ([#24]).
- The Phase A shared catalog schema and migrations: deployments, instances,
  hosts, snapshots, opaque session identity, server-time fenced host leases,
  and idempotency keys, applied transactionally from embedded SQL ([#25]).
- **`archive push` now publishes to the shared catalog.** Until now shared mode
  was configurable but inert: nothing registered a deployment, host, or
  instance, and no code path called publication or leases at all, so an
  operator's machines could never appear in the shared catalog. A push now
  registers its identity, takes a server-time fenced host lease, publishes the
  snapshot with its session identity rows, and releases the lease. The restic
  snapshot id keys the publication, so a retried push of the same snapshot is a
  no-op rather than a duplicate.

  Only opaque identity crosses the boundary: a session's uid is a digest over
  deployment, host, harness, and source id, and the source id - which embeds a
  workspace-derived project slug - never leaves the machine (SPEC.md §9).

  **An outage defers rather than fails.** The snapshot is already durable in the
  repository, so a push that cannot reach PostgreSQL reports `uncatalogued` and
  exits 0, and the next push adopts it from the repository's snapshot listing.
  A lease another instance already holds defers the same way, for the same
  reason. What does *not* defer is a refusal: a rejected credential, a missing
  privilege, a pending migration, or a schema this binary cannot write all fail
  loudly, because reconciliation would hit the same wall and reporting a state
  that appears to resolve itself would hide a misconfiguration. The rule is
  whether PostgreSQL answered.

  The word is `uncatalogued`, matching `archive status`, and deliberately not
  `catalog-pending`: that phrase names a different state in this system — a row
  that exists, carries restic's real counts, and lacks any record of which
  sessions the snapshot held. A push that used the narrower word for the wider
  state would send an operator hunting for session detail that was never
  written rather than for a row that was never created.

  **Reconciliation runs before publishing, not after**, which is load-bearing
  rather than stylistic. `Reconcile` assigns each adopted snapshot the next
  order above the current maximum, so adopting a stranded *older* snapshot after
  publishing a newer one would give the older one the *higher*
  `publication_order` - and that column exists so readers can select the newest
  snapshot without trusting clock skew. Two tests pin it, one asserting the
  invariant and one demonstrating the inversion the sequence avoids.
- Session closure counts (artifacts, blobs, unresolved blob references) are
  cached in the local session catalog, at schema 2. Publication needs them on
  every push, and re-describing an unchanged session to recover a number the
  describe already computed would make an hourly push scale with the whole
  corpus rather than with what changed. A cache at the old schema is discarded
  and rebuilt, which is safe: every row derives from live sources.
- **`archive status` reports how far the shared catalog is behind**, so
  `catalog-pending` is observable between pushes rather than only in the output
  of the push that deferred. It reports whether the catalog is reachable, how
  many snapshots are archived but uncatalogued, and how many are recorded
  without session rows.

  SPEC.md §9 promised an idempotent local `catalog-pending` journal; that is now
  scoped to Phase B's `pending-sync`, and Phase A derives the answer instead.
  The repository is authoritative for which snapshots exist and the catalog for
  which it recorded, so their difference *is* the state. A third local copy
  could be lost with the rebuildable local database, or go stale the moment
  another instance reconciles, and would then disagree with both.

  An unreachable catalog leaves the counts **absent rather than zero**:
  reporting 0 uncatalogued snapshots is a claim the command cannot make without
  reading the catalog, and the terminal output says `unknown`.

  The two counts are **not interchangeable**, and the difference decides what
  an operator should do. An uncatalogued snapshot has no catalog row, which is
  what an outage leaves behind, and the next push records it. A
  `catalog-pending` row already exists with real counts from restic but no
  record of which sessions the snapshot held — and no shipped command resolves
  that, because pushing again publishes the next snapshot rather than
  completing this one, so the count does not fall. The archive is unaffected:
  those snapshots stay durable and restorable, and only catalog detail about
  them is missing. Completing it needs a restore-and-rescan, now an explicit
  Phase C item rather than something SPEC.md implied a push would do.
- **Babel owns its own PostgreSQL schema** (`babel`), created by `storage
  migrate` and pinned as `search_path` on every connection (decision 47).
  Driving the real Clever Cloud add-on showed why: it pre-installs 40
  extensions, and PostGIS and `pg_stat_statements` put 7 relations in `public`.
  The allowlist gate — which makes the SPEC.md §9 plaintext boundary
  enforceable — saw those as unauthorized tables and failed the migration
  *after* it had applied. Sharing a schema leaves no way out: rejecting a
  provider's extensions is wrong, and ignoring unknown tables blinds the gate
  to a Babel migration adding an unlisted one. An owned schema keeps it exact.
  An unknown table is now also named once rather than once per column.
- **Shared mode in `storage.json`, at schema 2.** The document now carries
  `mode`, deployment/instance identity, and a `catalog` section: PostgreSQL
  endpoint, TLS mode with an optional root CA, one credential by default, and
  an optional separate migration credential where a provider can issue one. A
  schema-1 document loads as local mode, so existing configurations keep
  working; a newer schema is refused by name. Golden fixtures cover local,
  single-credential shared, and separate-migration-credential documents.
- **Credential privileges are detected rather than assumed** (SPEC.md §9,
  decision 46). `storage verify` reports what the credential is observed to
  hold — superuser, role-creating, DDL, or application — read from PostgreSQL's
  own catalogs with no destructive probe. Role attributes are not inherited
  through membership in PostgreSQL, so the check tests reachability by
  `SET ROLE` rather than inherited usage; the inherited-usage form silently
  reports a `NOINHERIT` member of a superuser role as an application
  credential, which was confirmed on a throwaway cluster before choosing.
- `storage migrate`, which applies pending migrations with the configured
  credential by default, or with an ephemeral document's migration credential
  that is used and never persisted. `storage verify` checks a configured
  catalog live: TLS as the server reports it, observed privileges, and schema
  compatibility.
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
- **Two-instance acceptance now runs as a test** (SPEC.md §10's pre-deployment
  gate: a second independently configured instance must browse the shared
  catalog, fetch a session the first host archived, lose and rebuild its local
  SQLite cache, and recover cleanly). Two instances with their own HOME, XDG
  configuration/data/cache, host identity, and instance id share exactly what a
  real deployment shares — one restic repository and one PostgreSQL catalog —
  and the scenario proves, in order: both configure independently while only
  one migrates; host A publishes; host B browses the catalog it never wrote to;
  B publishes its own session and the catalog holds two hosts, ordered, with
  distinct session identities; B lists and then byte-exactly fetches A's
  session into B's own store, and A does the same for B's, since neither
  instance is privileged; B loses its local catalog and rebuilds it field for
  field, while the shared view and the already-materialized session — retained
  data, not cache — are untouched; a publication lease with a live owner defers
  the other claimant's push, leaving the snapshot durable and reported
  `uncatalogued`; and the next uncontended push adopts it with monotonic
  publication order.

  The catalog is reached through Babel's own configuration document, so the
  connection is TLS — shared mode cannot express `sslmode=disable`. A new
  `internal/pgtest` provisions throwaway clusters with a self-signed
  certificate for that reason, and its own tests assert the server's view of
  the connection (`pg_stat_ssl`) rather than trusting the client's request. A
  server without `ssl=on` refuses a `require` client outright — measured:
  `server does not support SSL, but SSL was required` — so a successful
  connection already proves encryption; what the server's view adds is the
  negotiated protocol, which is the same thing `storage verify` reports to an
  operator rather than echoing the mode that was asked for. `internal/sharedcatalog`'s harness now provisions through the
  same package.
- **`archive status` reports what the shared catalog holds, per host**, so a
  second instance can browse it rather than only compare totals against the
  repository. A second table lists each publishing host's catalog snapshot
  rows, distinct session identities, catalog-pending rows, newest publication
  order, and that row's snapshot time. It stays a separate table from the
  repository listing on purpose: the whole point of the column is that the
  repository and the catalog can disagree, and merging them would hide it.
  Absent in local mode, and absent rather than empty when the catalog could not
  be read.
- **`babel sessions list --host HOST [--snapshot ID]`** lists the sessions
  another host archived, which selective cross-host fetch needed to be usable:
  `fetch --host` already worked, but nothing could discover a selector to hand
  it. The listing reads only the snapshot's file tree — no transcript bytes are
  downloaded — so it reports harness, source id, selector, and primary size,
  and leaves title, workspace, modification time, and continuation grade
  absent, rendering `-`. Selectors are identical to the ones a local listing
  gives the same sessions, so an operator has one selector vocabulary whether
  the files are here or only in the archive. `--host` is rejected with
  `--roots` and `--no-cache`, and `--snapshot` without `--host`, each naming
  the conflict rather than silently preferring one source.
- **Direct recovery is now tested with `restic` alone** — no Babel in the
  restore path, no PostgreSQL, no configuration: just the repository locator
  and the password file, then a byte-for-byte comparison of every regular file
  under every backup root the push reported. This is the guarantee that makes
  the archive trustworthy independently of Babel (SPEC.md §14), and it was the
  one §14 leg with no coverage at all. Restoring through a Babel helper could
  have passed while the property failed, so the test shells out to the real
  binary. It walks the sources rather than the restore, since the reverse
  comparison would pass for a restore that dropped files, and it fails if the
  walk compared implausibly few files.
- **Two more §14 pre-deployment gates now have tests**, both reachable without
  the provider.

  *Idempotent concurrent writers*, proven with two operating-system processes
  overlapping in time rather than two sequential calls — it has to be out of
  process, since Babel resolves HOME and the XDG roots from the environment and
  that is process-global. Same host: a lease serializes writers rather than
  refusing them, so both committing is legal if the first released before the
  second asked; what must hold is that no push fails, every outcome is a state
  the catalog can be in, both snapshots reach the repository regardless of who
  won, and one later push settles whatever the race left. Different hosts: both
  commit, with separate leases and separate publication-order sequences. This
  test is what surfaced the repository-initialization hazard below.

  *Complete catalog rebuild from the repository snapshot list plus source
  rescans*, proven by destroying the catalog outright — `DROP SCHEMA babel
  CASCADE`, not a truncation — and recovering with only the documented path:
  migrate, then push. No catalog backup is assumed, because none is promised.
  What returns is snapshot visibility, ordering, restic's counts, and current
  session identity from the rescan; what does not is which sessions each
  *historical* snapshot held, so those rows come back `catalog-pending` and
  stay there. The test asserts that asymmetry rather than a total, which is the
  difference between checking a rebuild and checking a number.
- **`babel storage rebuild --host HOST --yes`** exposes the catalog rebuild
  SPEC.md §12 lists as a Phase A deliverable. `sharedcatalog.Rebuild` had
  implemented it correctly, with tests, since the schema landed — and no command
  could invoke it, so it was reachable only from its own unit tests. A
  deliverable nothing can reach is not delivered.

  Its doc comment also called it *"the recovery path that makes losing the Phase
  A database survivable"*, which the rebuild gate had just disproved by
  recovering without it: an empty catalog needs only `storage migrate` and each
  host's next push. Rebuild is the **repair** path, for rows that are present
  but wrong — which no push corrects, because a push appends its own snapshot
  rather than auditing the ones already recorded. The doc now says that.

  `--host` is required rather than defaulting to this machine, and `--yes` is
  required too, because the command discards derived rows and the wrong host
  would be a silent loss. An unknown host is refused naming the hosts that do
  exist, since a mistyped one would otherwise rebuild to empty. Ordering is
  rederived from restic's recorded times; session rows are discarded rather than
  invented, so the snapshots come back `catalog-pending` and identity returns
  with the owning host's next push.
- **Babel can reach an object store at all**, which it could not before. The
  restic child process gets a deliberately strict environment allowlist —
  `RESTIC_REPOSITORY`, `RESTIC_PASSWORD_FILE`, `RESTIC_CACHE_DIR`, `HOME`,
  `PATH`, `TMPDIR` — carrying no access key, and the storage document had no
  field for one. Every archive test ran against a local path, which needs no
  credential, which is exactly why nothing caught it. "Nothing has written a
  byte to Cellar" was not caution; it was impossible.

  `repository_store.access_key_id` and `repository_store.secret_access_key` now
  live inline in the document beside the catalog's password (decision 50). They
  are required for an `s3:` locator, refused in halves, and refused as an empty
  block, because deferring a credential error to the first backup is the worst
  moment to find it. They reach restic as `AWS_ACCESS_KEY_ID` and
  `AWS_SECRET_ACCESS_KEY`: restic offers no file reference for them the way it
  does for the repository password, so this is the one secret this path puts in
  a child environment, and the reason is recorded where the code does it.

  Proven against the real Clever Cloud Cellar add-on with synthetic fixtures:
  `archive init` created the repository, a push committed a snapshot and
  published its session rows to the real managed PostgreSQL, `verify` passed
  over S3, a second independently configured instance browsed the catalog and
  listed and fetched the first host's session, and **restic alone restored all
  five files byte-identically** with no Babel and no PostgreSQL involved.
  Neither credential nor the repository password appeared anywhere in the
  captured output.
- **The web lock/stop control**, the last named Phase A deliverable that was
  absent (§12's deliverable bullet, §2's contract, decisions 34 and 45). A
  same-origin `POST /api/lock` revokes the launch token **before** the listener
  closes, so a winding-down process cannot still honour it, and the page
  replaces its whole shell with a terminal state rather than appearing to work.
  `babel web` exits 0 when the operator asked it to stop.

  Implementing it surfaced that **`Host`/`Origin`/DNS-rebinding checks did not
  exist** — decision 34 requires them and the bearer token was carrying the
  whole CSRF defence alone. There is now one shared guard for every `/api` path,
  checked before the credential is read: `Host` must be the loopback literal,
  and `Origin`, when a browser sends it, must match. The token remains the
  primary defence; this closes the weaker signal that decision 34 names, and it
  matters most for lock/stop, where a forged request is a denial of service.

### Removed

- **`babel status` from §8's command list.** It appeared exactly once in the
  whole specification, with no behavioural rule, no §12 deliverable, and no
  acceptance text, at the tail of the Phase B commands. Bare `babel` is the
  offline status overview and has a rule, §8.1, and decision 12 behind it, so a
  second command with none of that was a list artifact rather than unbuilt work
  (operator decision 2026-08-29).

### Changed

- **The spec no longer assumes a pre-Babel backup job running behind Babel.**
  Six places did, including decision 4's "never deleted" clause and §12's
  rollback contract. Retiring that job is a per-machine dotfiles cutover and not
  something any Babel command does, so what §12 now records is the consequence
  that is Babel's: a deployment with no legacy job behind it has no second
  automated copy, and a generation from before the cutover reinstates an archiver
  Babel does not coordinate with (decision 52).

- **`archive push` no longer creates the repository; `babel archive init` does,
  once per deployment.** Auto-init on push was a data-loss hazard on the
  unattended path, found by writing the concurrent-writer test.

  restic generates a master key per `init` and writes the key before the
  config, so two inits racing on an empty repository **both succeed** and leave
  two valid keys with one config. restic then selects a key by iteration and
  fails outright when it picks the wrong one. Measured against restic 0.19.1:
  10 of 10 races left two keys, and 7 of 10 subsequent backups failed with
  `config or key <id> is damaged: ciphertext verification failed` — a
  repository needing manual repair. Two machines' hourly timers firing together
  at a new Cellar repository is exactly that race.

  The second hazard is worse: silent creation turned a **mistyped locator** into
  a brand-new empty archive. Hourly pushes would keep reporting success into it
  while the real archive appeared to stop growing — a failure that reports
  success, which is worse than one that stops.

  So `push` now calls `Require` and fails with `no repository at <locator>: run
  `babel archive init` once for this deployment`, leaving nothing behind. A
  repository that exists but does not open is reported as *that*, not as
  needing initialization, because initializing over a real repository whose
  password is wrong would answer a credential problem destructively.

  The existing `TestEnsureInitUnderConcurrentCallers` asserted this was safe
  and passed while the hazard was real: it checked that no error came back, not
  that the resulting repository was usable. It is replaced by a test of the
  property `push` now depends on — that a missing repository is distinguishable
  from every other failure.
- **The first deployment's catalog connection is encrypted but not
  authenticated, and SPEC.md now says so** (decision 48). Clever Cloud's
  managed PostgreSQL presents a self-signed certificate with **no
  subject-alternative name**, whose common name identifies a different
  instance than the one it serves — so `verify-full` cannot succeed there, and
  pinning the certificate would not supply the missing name. `require`
  negotiates TLS 1.3 and is the honest setting; the residual exposure is an
  attacker on the network path impersonating the database and capturing the
  catalog credential, bounded by Phase A sending only opaque identifiers,
  ordering, counts, commit state, and timestamps.

  Babel's own behaviour needed no change, which was worth confirming rather
  than assuming: its error names the real defect (`certificate is not valid
  for any names`), and `verify-full` was separately proven to **accept** a
  trusted chain with a matching hostname and to refuse on both CA and hostname
  grounds, so its rejections discriminate instead of being unconditional.
- `DetectPrivileges` no longer under-reports on a database Babel has never
  migrated. `current_schema()` resolves to nothing before the schema exists, so
  the schema-CREATE check reported `application` for a credential that can in
  fact set the deployment up; it now falls back to CREATE on the database,
  which is exactly the right required to create Babel's schema. This is the
  state `storage configure` runs in on a first deployment, and a real add-on
  reaches it.
- `AcquireHostLease` now refuses write authority against an incompatible
  schema rather than leaving the check unwired, so a binary cannot publish into
  a database migrated by a newer one. This is deliberately *not* downgrade
  protection: an older binary performs no version check, so nothing in Go
  constrains it — only what PostgreSQL evaluates for it, such as a lease's own
  SQL expiry predicate. The schema stays at version 1; an unmigrated database
  is named as such, with the command that fixes it, rather than surfacing
  whichever missing relation a query happened to hit first.
- Errors that carry a connection string now redact the password on its own as
  well as the whole DSN. A driver may reconstruct a connection string from
  parsed fields rather than echo the one it was given, and the whole-string
  replacement could not match that; pgx happens to omit the password when it
  does so, which made the guarantee depend on a dependency's discretion.
  Mutation-tested: both new arrangements survive redaction under the previous
  implementation.
- `storage verify` reported "pending migration: yes" on a catalog it had just
  finished migrating. It inferred pending-ness from the deployment's recorded
  `schema_version`, which answers a different question and is written at first
  publication rather than by migrating, so the two sources disagree in exactly
  the state a first-time operator sees. Pending migrations now come from the
  migration ledger, and a version of 0 renders as "not recorded yet" rather
  than as a bare zero beside a compatible schema. Found by driving the real
  binary against a live TLS-enabled PostgreSQL, not by reading the code.
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
  Per-instance eviction is therefore absent rather than replaced. An
  application-level `revoke-instance` was built, measured, and **removed
  before shipping** (operator decision, 2026-08-29): revocation is ordinary DML
  on `instances` — a table every instance must already write to register
  itself — so any credential that can publish could revoke any instance and
  clear its own revocation, which a test demonstrated directly. A control whose
  authority cannot be authenticated reads as containment without being it, and
  the honest alternative is not to offer it. A retired machine's slot now frees
  when its lease expires. Whether per-instance eviction should exist at all,
  enforced by column-level grants and per-instance roles, is a §14
  pre-deployment decision and §12 Phase C work.
  Role separation stays specified and implemented for providers that
  permit it; granting least privilege to a provider-created user is explicitly
  untested on Clever Cloud and may not be relied on until proven against the
  real add-on (decision 46).

### Fixed

- **The end-to-end suite could have read the operator's real `storage.json`.**
  `newEnv` isolated HOME, `XDG_DATA_HOME` and `XDG_CACHE_HOME` but not
  `XDG_CONFIG_HOME`, and `os.UserConfigDir` prefers that variable over HOME. It
  was harmless only because no production configuration existed anywhere — and
  the very next step is creating one. A shared-mode document carries a real
  repository locator and a real catalog DSN, so any command resolving through
  configuration rather than explicit flags would have addressed the operator's
  actual Cellar bucket and PostgreSQL from a test run.

  `internal/cli`'s fixture already carried this guard, its comment recording
  that two unrelated tests once observed a configuration they never wrote; the
  e2e suite drives the same commands and lacked it. Reverting the one line makes
  the new regression test fail with `Fatal: /nonexistent/outside-password does
  not exist` while reaching for an `s3://outside.invalid/operator-bucket`
  locator, which is the hazard stated exactly.

- **A session's catalog identity was derived from `storage.json` rather than
  from the host actually publishing it.** The snapshot goes to restic under the
  resolved host — `--host`, else `$BABEL_HOST_ID`, else the configured value —
  and that resolved host takes the publication lease and owns the snapshot row,
  but the session-identity digest was computed from the configured `host_id`
  alone. Any override silently attributed a host's sessions to an identity that
  never published them, and two hosts archiving the same source tree collided
  on one digest instead of producing two, which is the uniqueness the digest
  exists to provide (decision 9). Session identity now follows the publishing
  host, and a test pushes one source session under two host identities and
  asserts the catalog holds two distinct identities — it holds one without the
  fix. The two-instance acceptance cannot catch this on its own, since its two
  hosts archive disjoint sessions.
- **The end-to-end suite flaked about one run in six, and the assertion was
  wrong rather than the code.** It appends to a session log and requires the
  next push to add fewer bytes than the file, which states deduplication. That
  only holds if the file spans more than one chunk, and the fixture was 4.34 MB
  against restic's 8 MiB maximum chunk size — so under some repositories the
  whole file was a single chunk and appending genuinely re-stored all of it.

  The randomness was not in the content, which is fixed-seed: **restic picks a
  chunker polynomial per repository**, and every run builds a fresh one.
  Measured across eight runs of byte-identical input, the second push added
  between 166 KB and 4.57 MB — and that upper figure exceeds the old fixture,
  which is exactly the observed failure. The padded log is now 11.12 MiB, above
  the 8 MiB bound, so at least two chunks exist under every polynomial and the
  assertion is an invariant instead of a coin flip. The test asserts that
  precondition itself, so shrinking the fixture fails loudly rather than
  quietly restoring the flake, and it logs the accounting so any future tighter
  bound can be argued from observation.
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
