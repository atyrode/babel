-- Fleet presence: what is running where, visible from any machine (issue #118).
--
-- WHY THIS TABLE EXISTS AT ALL
--
-- Babel is machine-agnostic everywhere the data plane is concerned: records,
-- receipts, dispositions and the frontier all sync fleet-wide, so any
-- authorized instance can read what any other instance produced. What stayed
-- host-bound was the interval before that: a conductor cycle or an explore run
-- is invisible off-host until its receipt commits, which can be an hour of
-- model work later. An operator at another machine cannot tell a working fleet
-- from an idle one, and that is a legibility gap rather than a data gap.
--
-- WHY IT IS POSTGRESQL-ONLY, AGAINST THE OBJECT-FIRST DISCIPLINE
--
-- Every other Phase B row in this schema is the database-last half of a
-- commit protocol: the payload is sealed into an object, read back, verified,
-- and only then does a row name it (0003_phase_b_records). That discipline
-- exists because a hypothesis, finding or receipt exists nowhere but Babel, so
-- losing it is unrecoverable.
--
-- A presence row is the opposite kind of fact. It says "a run said it was
-- alive at this instant", it is worthless five minutes after the process it
-- describes exited, and it is recoverable in the only sense that matters: the
-- run itself carries on, its receipt commits through the normal protocol, and
-- the durable record of what happened is that receipt. So there is deliberately
-- no object-store leg here and no local durable copy: an object write per
-- heartbeat would spend real money and latency to durably preserve a statement
-- whose whole value is that it is current. PostgreSQL alone is the right
-- storage for ephemeral status, and this is the one table in the schema where
-- that is true.
--
-- WHY IT IS UPDATE-ABLE, AGAINST THE APPEND-ONLY DISCIPLINE
--
-- 0003 and 0008 install triggers that refuse UPDATE and DELETE outright,
-- because an analysis record is a claim and a wrong claim is answered by a
-- later record, never by editing one. A heartbeat is not a claim about the
-- corpus; it is one row's liveness, and the append-only form of it would be a
-- row per heartbeat - thousands of rows per run, each superseding the last, so
-- that every reader has to reduce them to the one fact the newest row already
-- holds. That is worse in every dimension and no more honest.
--
-- So this table admits UPDATE, and the trigger below narrows it to exactly the
-- four columns that may move: heartbeat_at, state, finished_at,
-- receipt_record_id. An announcement's own content - who, where, which run,
-- which recipe, under what authority, from when - is fixed at announce time and
-- the database refuses to let it drift. heartbeat_at may only advance, and a
-- row that has left `running` is final: a zombie process cannot resurrect a
-- finalized run, and a finalized run cannot be re-attributed.
--
-- DELETE is left alone rather than blocked, and that is not an oversight. No
-- Babel code path deletes a presence row - there is no reaper, by decision:
-- readers classify a row by heartbeat age and render staleness honestly ("last
-- seen 7m ago; running or dead, this host cannot tell"), which is strictly more
-- information than a deleted row conveys, and a reaper would be a process
-- asserting a death it cannot observe. But this is disposable status, so an
-- operator with psql should be able to clear it, and a trigger claiming
-- otherwise would be defending durability the data does not have.
--
-- WHAT IS AND IS NOT STORED
--
-- Identifiers, closed vocabularies and timestamps. No content of any kind: no
-- note, no cycle reason, no failure text, no session title, no workspace path.
-- Two columns are worth naming because they look like prose and are not.
-- `recipe` is a cookbook recipe's name, which is guidance vocabulary on
-- 0001_init's own terms for `harness` - it says which lens a run applied, never
-- anything about what the run read or concluded. `authority_kind` and
-- `authority_ref` are #96's pair: three compile-time kinds, and a reference
-- that internal/run already constrains to identifier shape - an invitation id,
-- a policy name, a draw id. The receipt carries the same pair in its own
-- header for exactly this reason (SPEC.md 9's plaintext allowlist), so nothing
-- new crosses the boundary here.
--
-- Also absent: any second copy of host identity. host_id is the same opaque,
-- operator-assigned value `hosts.host_id` and `snapshots.host_id` carry, and a
-- render surface that wants a display name joins `hosts` for it. A system
-- hostname is infrastructure identity and stays outside this schema.
--
-- WHY THERE ARE NO FOREIGN KEYS
--
-- deployment_id, host_id and receipt_record_id all name rows that plausibly
-- exist in this database, and none of them is declared as a reference. That is
-- deliberate and it is the whole shape of the feature: a presence write must
-- never fail, slow, or block the run it describes. `hosts` and `deployments`
-- are written by Register on a machine's first archive push, so a machine that
-- has analysed but never pushed would have its runs refused by referential
-- integrity - the run would proceed anyway, since every write here is
-- best-effort, but the fleet would silently never see that host. A row naming
-- an unregistered host is more useful than no row. receipt_record_id is the
-- same argument at the other end: the receipt commits through 0003's protocol
-- on its own schedule, and presence must be able to record which record id the
-- run finished under before, or without ever, that commit landing.
--
-- SchemaVersion stays 1: this is additive, it constrains no existing writer,
-- and EnsureCompatible refuses a database migrated past the binary, so raising
-- it would stop every live Phase A writer against production (SPEC.md 14).

CREATE TABLE presence (
    -- Client-generated, 128 random bits behind a kind prefix: the same shape
    -- internal/frontier, internal/reality and internal/reference mint, and what
    -- lets a host announce without coordinating for an identity first.
    presence_id       text        PRIMARY KEY,
    deployment_id     text        NOT NULL,
    host_id           text        NOT NULL,
    -- The two things that announce. A conductor cycle and an explore run are
    -- different facts about the fleet - one is a loop deciding, the other is
    -- work happening - and a reader that could not tell them apart would show
    -- a scheduling tick as analysis.
    kind              text        NOT NULL CHECK (kind IN ('conductor', 'explore')),
    run_id            text        NOT NULL,
    -- The primary recipe, NULL when the run named none. Singular by design:
    -- the receipt is where a run's whole cookbook set is recorded, and
    -- presence answers the narrower question of what a fleet row is for.
    recipe            text,
    -- The preparation the run is scoped to, NULL before one exists: a
    -- conductor cycle announces before its runner has prepared anything.
    preparation_id    text,
    -- #96's pair. NULL means unrecorded, which is what Authority.Recorded()
    -- reports, rather than an empty string a reader must know to read as
    -- absence.
    authority_kind    text        CHECK (authority_kind IS NULL OR authority_kind IN
                          ('operator', 'policy', 'serendipity')),
    authority_ref     text,
    state             text        NOT NULL CHECK (state IN
                          ('running', 'finished', 'failed', 'cancelled')),
    -- Server time throughout (SPEC.md 9). A host with a skewed clock must not
    -- be able to announce itself permanently fresh, and staleness is computed
    -- as now() - heartbeat_at inside the read query for the same reason: no
    -- client clock enters the classification at either end.
    started_at        timestamptz NOT NULL DEFAULT now(),
    heartbeat_at      timestamptz NOT NULL DEFAULT now(),
    -- Set exactly when the row leaves `running`, and the CHECK is what keeps
    -- the two from disagreeing: a finished row with no finish time, or a
    -- running row with one, would each be a state a reader has to guess about.
    finished_at       timestamptz,
    -- The receipt's Phase B record id, once the run has one. It is a plain
    -- opaque id rather than a reference for the reason given above, and it is
    -- what lets a fleet reader walk from "this run finished" to the durable
    -- record of what it did.
    receipt_record_id text,
    CHECK ((state = 'running') = (finished_at IS NULL)),
    CHECK (heartbeat_at >= started_at)
);

-- The two questions asked of this table. "What is happening on the fleet right
-- now" is a scan of live rows newest-heartbeat-first; "what has this host been
-- doing" is one host's rows in the same order. Both indexes carry heartbeat_at
-- descending because every read is ordered by it.
CREATE INDEX presence_host_idx ON presence (host_id, heartbeat_at DESC);
CREATE INDEX presence_state_idx ON presence (deployment_id, state, heartbeat_at DESC);

-- The narrowing that makes UPDATE safe here. Everything an announcement
-- asserted is immutable; the four liveness columns move; heartbeat_at only
-- advances; and a row that has left `running` is final.
CREATE FUNCTION presence_liveness_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.state <> 'running' THEN
        RAISE EXCEPTION
            'presence: % is finalized (state %); a finalized row is final, so a late heartbeat cannot resurrect it (issue #118)',
            OLD.presence_id, OLD.state;
    END IF;
    IF NEW.presence_id     IS DISTINCT FROM OLD.presence_id
    OR NEW.deployment_id   IS DISTINCT FROM OLD.deployment_id
    OR NEW.host_id         IS DISTINCT FROM OLD.host_id
    OR NEW.kind            IS DISTINCT FROM OLD.kind
    OR NEW.run_id          IS DISTINCT FROM OLD.run_id
    OR NEW.recipe          IS DISTINCT FROM OLD.recipe
    OR NEW.preparation_id  IS DISTINCT FROM OLD.preparation_id
    OR NEW.authority_kind  IS DISTINCT FROM OLD.authority_kind
    OR NEW.authority_ref   IS DISTINCT FROM OLD.authority_ref
    OR NEW.started_at      IS DISTINCT FROM OLD.started_at
    THEN
        RAISE EXCEPTION
            'presence: only heartbeat_at, state, finished_at and receipt_record_id may change; an announcement is fixed at announce time (issue #118)';
    END IF;
    IF NEW.heartbeat_at < OLD.heartbeat_at THEN
        RAISE EXCEPTION
            'presence: heartbeat_at only advances; a heartbeat that moved it backwards would make a stale writer look fresher than a live one (issue #118)';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER presence_liveness_only_trg
    BEFORE UPDATE ON presence
    FOR EACH ROW EXECUTE FUNCTION presence_liveness_only();
