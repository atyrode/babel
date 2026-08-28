-- Phase A shared catalog and coordination schema (SPEC.md 6.2, 9).
--
-- SPEC.md 9 permits exactly these classes of data in PostgreSQL: schema and
-- version identifiers, opaque IDs and locators, ordering, sizes and counts,
-- commit state, lease and fencing data, and timestamps. Titles, filesystem
-- paths, workspace names, and transcript metadata are excluded by contract and
-- live only in the encrypted restic repository, in live local sources, and in
-- decrypted local SQLite indexes.
--
-- Every column below is justified against that list, and allowlist_test.go
-- fails the build if a column appears in the database that is not enumerated in
-- allowlist.go. Adding a column is therefore a deliberate contract change.
--
-- COLUMN                          CLASS
-- *_id, session_uid               opaque ID or locator
-- schema_version, migration       schema/version identifier
-- harness                         schema identifier (adapter enum; see below)
-- publication_order, fence        ordering, fencing data
-- commit_state                    commit state
-- files_*, bytes_*, *_size,
--   *_count                       sizes and counts
-- *_at                            timestamps
--
-- Three exclusions are deliberate and easy to reintroduce by accident:
--
--   * A session's real selector (for example "omp-babel/2026-...") embeds a
--     project slug derived from its workspace directory, so it is NOT stored.
--     Sessions are identified by session_uid, an opaque digest. Resolving a uid
--     back to a selector requires the repository or a local index.
--   * A host's display name is not in the allowlist. Hosts are keyed by their
--     operator-assigned host_id; the human-facing name lives in restic snapshot
--     metadata inside the encrypted repository.
--   * No session quality verdicts. Continuation grade is derived locally from
--     unresolved_blob_count and artifact closure; storing the conclusion would
--     put a judgement about transcript content in the shared catalog.
--
-- `harness` is admitted as a schema identifier rather than user data: it names
-- which adapter schema a row follows, ranges over three compile-time constants,
-- and is required by decision 8's per-adapter coverage reporting. It reveals
-- only which harnesses Babel supports, which is public.

CREATE TABLE deployments (
    deployment_id   text        PRIMARY KEY,
    schema_version  integer     NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE instances (
    instance_id     text        PRIMARY KEY,
    deployment_id   text        NOT NULL REFERENCES deployments (deployment_id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX instances_deployment_idx ON instances (deployment_id);

CREATE TABLE hosts (
    host_id         text        PRIMARY KEY,
    deployment_id   text        NOT NULL REFERENCES deployments (deployment_id),
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX hosts_deployment_idx ON hosts (deployment_id);

-- One row per restic snapshot Babel has published or discovered.
--
-- The repository is archive truth: a snapshot exists here only because restic
-- committed it. commit_state carries reconciliation state - 'catalog-pending'
-- means restic holds the snapshot but its session rows are incomplete, which
-- any authorized instance may reconcile; reconciled_at records when an instance
-- last checked it against the repository snapshot list.
--
-- publication_order is assigned by the owning host and totally orders that
-- host's snapshots, so readers select a newest snapshot without trusting clock
-- skew between machines.
CREATE TABLE snapshots (
    snapshot_id         text        PRIMARY KEY,
    host_id             text        NOT NULL REFERENCES hosts (host_id),
    publication_order   bigint      NOT NULL,
    snapshot_time       timestamptz NOT NULL,
    commit_state        text        NOT NULL
        CHECK (commit_state IN ('catalog-pending', 'committed')),
    files_new           bigint      NOT NULL DEFAULT 0,
    files_changed       bigint      NOT NULL DEFAULT 0,
    files_unmodified    bigint      NOT NULL DEFAULT 0,
    bytes_added         bigint      NOT NULL DEFAULT 0,
    session_count       integer     NOT NULL DEFAULT 0,
    published_by        text        REFERENCES instances (instance_id),
    reconciled_at       timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (host_id, publication_order)
);

CREATE INDEX snapshots_host_order_idx ON snapshots (host_id, publication_order DESC);
CREATE INDEX snapshots_pending_idx ON snapshots (commit_state) WHERE commit_state = 'catalog-pending';

-- Session identity, opaque by construction.
--
-- session_uid is a digest over deployment, host, harness, and the adapter's
-- source id. It is stable across pushes for the same session and reveals
-- nothing about the workspace.
CREATE TABLE sessions (
    session_uid             text        PRIMARY KEY,
    host_id                 text        NOT NULL REFERENCES hosts (host_id),
    harness                 text        NOT NULL CHECK (harness IN ('omp', 'codex', 'claude')),
    first_snapshot_id       text        NOT NULL REFERENCES snapshots (snapshot_id),
    latest_snapshot_id      text        NOT NULL REFERENCES snapshots (snapshot_id),
    primary_size            bigint      NOT NULL DEFAULT 0,
    artifact_count          integer     NOT NULL DEFAULT 0,
    blob_count              integer     NOT NULL DEFAULT 0,
    unresolved_blob_count   integer     NOT NULL DEFAULT 0,
    source_modified_at      timestamptz,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX sessions_host_idx ON sessions (host_id);
CREATE INDEX sessions_latest_snapshot_idx ON sessions (latest_snapshot_id);

-- Server-time fenced host leases.
--
-- A host's rows are written by one instance at a time. now() is evaluated by
-- PostgreSQL, never by the client, so an instance with a skewed clock cannot
-- extend its own lease. fence increases monotonically per host: a writer that
-- resumes after its lease expired carries a stale fence and is rejected.
CREATE TABLE host_leases (
    host_id         text        PRIMARY KEY REFERENCES hosts (host_id),
    holder_id       text        NOT NULL REFERENCES instances (instance_id),
    fence           bigint      NOT NULL,
    acquired_at     timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL
);

-- Idempotency keys make a retried publication a no-op rather than a duplicate.
CREATE TABLE idempotency_keys (
    idempotency_key text        PRIMARY KEY,
    instance_id     text        NOT NULL REFERENCES instances (instance_id),
    snapshot_id     text        REFERENCES snapshots (snapshot_id),
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);
