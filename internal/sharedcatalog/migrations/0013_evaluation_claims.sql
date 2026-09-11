-- Fleet-wide evaluation admission and spend (SPEC.md 4.12, 5.8, 9).
--
-- These are coordination tables, not Phase B analysis output. They contain
-- assignment ownership, revision identifiers, leases and monetary reservations;
-- assessment content, votes, reasons and scores remain in sealed records.
--
-- Claim and Finish serialize on the deployment row before reading server time,
-- inspecting fences or changing spend. The day row persists the effective UTC
-- allowance. Its foreign key ensures referential integrity, not budget
-- admission: every application writer must use the coordination transaction.
--
-- Each ceiling is the minimum offered during that day. Lower limits apply even
-- when the accompanying assignment is refused; increases wait for the next UTC
-- day. Daily and per-cycle bounds reconcile independently, so the effective
-- pair need not equal one policy's requested pair.
--
-- Takeover appends a strictly newer fence. Unfinished attempts, including
-- superseded ones, remain charged at their full reservation. Finish records
-- actual spend, including overruns, only for the current authority. Identical
-- completion retries preserve the same receipt; a different cost is a conflict.
--
-- Updates allow only tightening a day and finishing a claim. Application code
-- never deletes these rows; DELETE remains available for operator-owned
-- retention/recovery rather than pretending to protect against the database
-- owner. Retained claim rows expose attention metadata, not review outcomes.
--
-- SchemaVersion remains 1: these migrations are additive. Compatibility gates
-- still refuse writes from binaries older than the applied migration ledger.

CREATE TABLE evaluation_budget_days (
    deployment_id  text        NOT NULL REFERENCES deployments (deployment_id),
    -- The server's UTC day. A date rather than a timestamp because it is a
    -- bucket, not an instant: two claims granted eleven hours apart share it.
    day            date        NOT NULL,
    -- The allowance in force, pinned by the day's first claim. daily_cost
    -- bounds the deployment across the day; per_cycle_cost bounds one worker
    -- run within it, which is how a single cycle is stopped from consuming the
    -- whole fleet's attention in one sitting (SPEC.md 5.8).
    --
    -- double precision for migrations/0006's reason, restated in
    -- evaluation_claims below: these are model costs, which arrive as floats
    -- the harness computed.
    daily_cost     double precision NOT NULL CHECK (daily_cost >= 0),
    per_cycle_cost double precision NOT NULL CHECK (per_cycle_cost >= 0),
    -- The policy version that most recently tightened either effective bound.
    -- The pair may incorporate lower ceilings from earlier policies that day.
    policy_version text        NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    -- When the pin last moved, which within a day can only mean it was
    -- tightened.
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (deployment_id, day)
);

-- The pin moves down, or not at all. This is the half of the one-allowance
-- guarantee that does not depend on the Go above it being correct: a writer
-- that believed a stale worker's larger ceiling cannot store it, so the worst a
-- wrong caller achieves is a refused claim rather than a raised budget. The day
-- and the deployment are the identity and never change; created_at is
-- first-seen and is left alone for the reason `hosts.created_at` is.
CREATE FUNCTION evaluation_budget_days_tighten_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.deployment_id <> OLD.deployment_id
       OR NEW.day        <> OLD.day
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION
            'an evaluation budget day is identified by its deployment and date; neither may change';
    END IF;
    IF NEW.daily_cost > OLD.daily_cost OR NEW.per_cycle_cost > OLD.per_cycle_cost THEN
        RAISE EXCEPTION
            'the allowance in force on %/% may only be tightened within the day (was %, % - offered %, %): within a day the catalog cannot tell an operator raising a budget from a stale worker presenting an outdated larger one, and a raise takes effect at the next UTC day (issue #219)',
            OLD.deployment_id, OLD.day,
            OLD.daily_cost, OLD.per_cycle_cost,
            NEW.daily_cost, NEW.per_cycle_cost;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER evaluation_budget_days_tighten_only_trg
    BEFORE UPDATE ON evaluation_budget_days
    FOR EACH ROW EXECUTE FUNCTION evaluation_budget_days_tighten_only();

CREATE TABLE evaluation_claims (
    deployment_id  text   NOT NULL,
    -- Minted by the selecting instance and deterministic in its inputs: the
    -- artifact revision reviewed, the review role, and the sample ordinal that
    -- distinguishes a second independent look from a duplicate of the first
    -- (SPEC.md 4.12). The catalog treats it as opaque and enforces only what it
    -- can: one live attempt per id, so the same assignment cannot run twice at
    -- once, while a later independent sample carries a different ordinal and is
    -- therefore a different id the fleet is free to grant.
    claim_id       text   NOT NULL,
    -- Per claim_id, strictly increasing, assigned by the server. It is the
    -- authority a worker presents, and the reason a resumed worker holding the
    -- right run id is still refused after a takeover.
    fence          bigint NOT NULL CHECK (fence > 0),
    -- What is being reviewed. The vocabulary is closed for 0010's reason: these
    -- are the artifact kinds evaluation covers, including a bounded
    -- meta-review of an evaluation itself, and a sixth value reaching this
    -- column would name a coverage lane nothing implements.
    subject_kind   text   NOT NULL CHECK (subject_kind IN
                       ('hypothesis', 'observation', 'finding', 'proposal', 'evaluation')),
    subject_id     text   NOT NULL,
    -- The worker run the authority is bound to, and the instance that asked.
    run_id         text   NOT NULL,
    owner_id       text   NOT NULL,
    -- Which versioned policy admitted this work, so a later reader can tell
    -- what the fleet was operating under when it spent (SPEC.md 5.8).
    policy_version text   NOT NULL,
    day            date   NOT NULL,
    -- What the claim is allowed to spend, and what it reported spending. The
    -- second is NULL until the attempt finishes, and its absence is exactly
    -- what "unobserved" means: the day's accounting charges the reservation
    -- for every attempt that never reported, live or expired.
    --
    -- double precision rather than numeric, on migrations/0006's reasoning: a
    -- model's cost arrives as a float the harness computed, and numeric would
    -- store it to an exactness the input never had while implying Babel priced
    -- something it did not.
    reserved_cost  double precision NOT NULL CHECK (reserved_cost >= 0),
    observed_cost  double precision          CHECK (observed_cost >= 0),
    state          text        NOT NULL CHECK (state IN ('claimed', 'finished')),
    claimed_at     timestamptz NOT NULL,
    expires_at     timestamptz NOT NULL,
    finished_at    timestamptz,
    PRIMARY KEY (deployment_id, claim_id, fence),
    -- Every reservation belongs to a persisted budget day. Admission itself
    -- requires the application transaction described above.
    FOREIGN KEY (deployment_id, day)
        REFERENCES evaluation_budget_days (deployment_id, day),
    -- A finished attempt has a finish time and an observed cost; an unfinished
    -- one has neither. Three columns that cannot disagree about which state the
    -- attempt is in.
    CHECK ((state = 'finished') = (finished_at IS NOT NULL)),
    CHECK ((state = 'finished') = (observed_cost IS NOT NULL)),
    -- A lease that expires at or before it was granted is not a lease.
    CHECK (expires_at > claimed_at),
    -- The day is the server's UTC day of the grant, not a label the writer
    -- chose. Without this the two could drift and the allowance would be
    -- spendable twice.
    CHECK (day = (claimed_at AT TIME ZONE 'UTC')::date)
);

-- The day total is the hot read: every claim sums its deployment's day before
-- deciding, and the per-cycle total narrows the same scan by run.
CREATE INDEX evaluation_claims_day_idx
    ON evaluation_claims (deployment_id, day, run_id);

-- The current owner of an assignment is its highest fence, which is a lookup by
-- the primary key's prefix; the partial index answers the narrower question a
-- claim asks first - is there a live attempt on this id - without scanning the
-- finished history of an id that has been sampled repeatedly.
CREATE INDEX evaluation_claims_live_idx
    ON evaluation_claims (deployment_id, claim_id, expires_at)
    WHERE state = 'claimed';

CREATE FUNCTION evaluation_claims_finish_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.state = 'finished' THEN
        RAISE EXCEPTION
            'evaluation claim %/% fence % is finished; a second finish reporting a different cost is a contradiction, not a correction (issue #219)',
            OLD.deployment_id, OLD.claim_id, OLD.fence;
    END IF;
    IF NEW.deployment_id  <> OLD.deployment_id
       OR NEW.claim_id    <> OLD.claim_id
       OR NEW.fence       <> OLD.fence
       OR NEW.subject_kind <> OLD.subject_kind
       OR NEW.subject_id  <> OLD.subject_id
       OR NEW.run_id      <> OLD.run_id
       OR NEW.owner_id    <> OLD.owner_id
       OR NEW.policy_version <> OLD.policy_version
       OR NEW.day         <> OLD.day
       OR NEW.reserved_cost <> OLD.reserved_cost
       OR NEW.claimed_at  <> OLD.claimed_at
       OR NEW.expires_at  <> OLD.expires_at THEN
        RAISE EXCEPTION
            'an evaluation claim attempt is immutable except for its finish: identity, day, reservation and lease may not change';
    END IF;
    IF NEW.state <> 'finished' THEN
        RAISE EXCEPTION
            'the only transition an evaluation claim attempt may make is claimed -> finished';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER evaluation_claims_finish_only_trg
    BEFORE UPDATE ON evaluation_claims
    FOR EACH ROW EXECUTE FUNCTION evaluation_claims_finish_only();
