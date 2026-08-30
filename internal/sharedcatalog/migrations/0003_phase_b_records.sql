-- Phase B durable analysis output: object-first, database-last commits
-- (SPEC.md 6.5, 7, 9, 12 Phase B).
--
-- Phase A's tables describe the archive. These two describe what exploration
-- produced, and they carry a different guarantee: a snapshot row is rebuildable
-- from the repository, while a hypothesis, finding, or receipt exists nowhere
-- else. That is why the payload is not here at all.
--
-- WHAT IS AND IS NOT STORED
--
-- A Phase B record's content is sealed into a randomized AEAD envelope and
-- written to the encrypted object store; PostgreSQL receives only the reference
-- to that object. SPEC.md 9's Phase B allowlist names exactly what may travel
-- in the clear - structured identifiers, entity kind and schema version,
-- encrypted-object references, key ID, ciphertext size, commit/sync state,
-- relationship IDs, ordering, counts and timestamps - and every column below is
-- one of those. There is deliberately no payload column: a sealed blob in
-- PostgreSQL would be defensible, but it would also be a place a future writer
-- could put something unsealed, and the object store is where large encrypted
-- material belongs anyway (SPEC.md 2.3).
--
-- object_digest is taken over the *sealed* object bytes, never over the
-- plaintext. A plaintext digest would be a deterministic function of the
-- payload, which is the search oracle SPEC.md 9 forbids: an observer could tell
-- that two rows hold the same claim without holding a key.
--
-- COMMIT MODEL
--
-- SPEC.md 6.5 requires a run to be durably committed only once its rows and its
-- objects have both committed, and an outage to leave staged output visibly
-- `pending-sync` rather than globally committed. So the run row is the
-- visibility boundary and carries the sync state, while a record row is written
-- only after its object has been read back and verified. A record row therefore
-- cannot name an object that is absent, and a reader treats a record as
-- globally reviewable only when its run says `committed`.
--
-- record_count is the run's declared closure. The flip to `committed` is
-- conditional on the catalog actually holding that many record rows, which is
-- what makes "a partial commit is not a commit" a database property rather than
-- a convention.
--
-- IMMUTABILITY
--
-- SPEC.md 4.7 states rejection never deletes a record, and 9 lists never
-- deleting remote analysis material among the invariants. Triggers enforce
-- that rather than trusting every future statement: a record is insert-only,
-- and a run may change only its sync state, only forwards.

CREATE TABLE analysis_runs (
    run_id              text        PRIMARY KEY,
    deployment_id       text        NOT NULL REFERENCES deployments (deployment_id),
    -- The instance that produced the run. It is provenance, not authority: any
    -- authorized instance may read, and continue from, a committed run.
    origin_instance_id  text        NOT NULL REFERENCES instances (instance_id),
    -- Repository-dependent work records an execution-host constraint
    -- (SPEC.md 9). NULL means the run is not pinned. The pin says which host
    -- can rerun the work; it never restricts who may read the result, which is
    -- the whole point of committing it globally (SPEC.md 4.7).
    execution_host_id   text        REFERENCES hosts (host_id),
    -- A relationship ID: the committed run this one continues, so a second
    -- instance's follow-on work is linked rather than merely adjacent.
    continues_run_id    text        REFERENCES analysis_runs (run_id),
    sync_state          text        NOT NULL
        CHECK (sync_state IN ('pending-sync', 'committed')),
    record_count        integer     NOT NULL CHECK (record_count > 0),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    committed_at        timestamptz,
    -- A committed run has a commit time and a pending one does not, so the two
    -- columns can never disagree about which state the run is in.
    CHECK ((sync_state = 'committed') = (committed_at IS NOT NULL)),
    CHECK (continues_run_id IS NULL OR continues_run_id <> run_id)
);

CREATE INDEX analysis_runs_deployment_idx ON analysis_runs (deployment_id);
CREATE INDEX analysis_runs_continues_idx ON analysis_runs (continues_run_id);
CREATE INDEX analysis_runs_pending_idx ON analysis_runs (sync_state)
    WHERE sync_state = 'pending-sync';

CREATE TABLE analysis_records (
    -- Globally unique and client-generated (SPEC.md 9), which is what makes a
    -- repeated commit a primary-key no-op instead of a second row.
    record_id       text        PRIMARY KEY,
    run_id          text        NOT NULL REFERENCES analysis_runs (run_id),
    -- Entity kind. SPEC.md 9 admits it in plaintext, and the CHECK keeps it a
    -- closed vocabulary: a new Phase B record type reaching the shared catalog
    -- is a migration and a review, not an unnoticed string.
    kind            text        NOT NULL CHECK (kind IN (
                        'hypothesis', 'observation', 'finding', 'proposal',
                        'link', 'disposition', 'context',
                        'preparation', 'receipt')),
    -- The record type's own schema version, independent of the catalog's:
    -- a hypothesis payload may evolve without telling Phase A writers to stop.
    record_schema   integer     NOT NULL CHECK (record_schema > 0),
    ordinal         bigint      NOT NULL CHECK (ordinal >= 0),
    -- Encrypted-object reference. Content-addressed by the sealed bytes, so a
    -- retry after a failed database step writes a new object rather than
    -- overwriting one a committed row may already name.
    object_key      text        NOT NULL,
    key_id          text        NOT NULL,
    ciphertext_size bigint      NOT NULL CHECK (ciphertext_size > 0),
    object_digest   text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, ordinal),
    UNIQUE (object_key)
);

-- Records are insert-only. An immutable record whose immutability depends on
-- nobody writing the wrong statement is not immutable.
CREATE FUNCTION analysis_records_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION
        'analysis_records is append-only: % refused (SPEC.md 4.7)', TG_OP;
END;
$$;

CREATE TRIGGER analysis_records_append_only_trg
    BEFORE UPDATE OR DELETE ON analysis_records
    FOR EACH ROW EXECUTE FUNCTION analysis_records_append_only();

-- A run's identity and declared closure are fixed at declaration; only the sync
-- state moves, and only from pending to committed. Without this, "committed"
-- would be a value any statement could take back, and the visibility boundary
-- SPEC.md 6.5 describes would not exist.
CREATE FUNCTION analysis_runs_forward_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION
            'analysis_runs rows are never deleted (SPEC.md 9)';
    END IF;
    IF NEW.run_id             <>               OLD.run_id
       OR NEW.deployment_id      <>               OLD.deployment_id
       OR NEW.origin_instance_id <>               OLD.origin_instance_id
       OR NEW.execution_host_id  IS DISTINCT FROM OLD.execution_host_id
       OR NEW.continues_run_id   IS DISTINCT FROM OLD.continues_run_id
       OR NEW.record_count       <>               OLD.record_count
       OR NEW.created_at         <>               OLD.created_at THEN
        RAISE EXCEPTION
            'analysis_runs identity and closure are immutable; only sync_state may change';
    END IF;
    IF OLD.sync_state = 'committed' AND NEW.sync_state <> 'committed' THEN
        RAISE EXCEPTION
            'a committed run never returns to pending-sync (SPEC.md 6.5)';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER analysis_runs_forward_only_trg
    BEFORE UPDATE OR DELETE ON analysis_runs
    FOR EACH ROW EXECUTE FUNCTION analysis_runs_forward_only();
